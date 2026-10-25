package secfacts

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
)

func TestFromInspectAllKinds(t *testing.T) {
	d := dockerapi.ContainerDetail{}
	d.ID = "abc123"
	d.Name = "/bad"
	d.Config.User = ""
	d.HostConfig.Privileged = true
	d.HostConfig.NetworkMode = "host"
	d.HostConfig.PidMode = "host"
	d.HostConfig.CapAdd = []string{"SYS_ADMIN", "CAP_NET_ADMIN"}
	d.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"}
	d.HostConfig.PortBindings = map[string][]dockerapi.PortBinding{
		"5432/tcp": {{HostIP: "0.0.0.0", HostPort: "5432"}},
		"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "8080"}},
	}

	got := FromInspect("abc123", d, CaddyPorts{})
	kinds := map[string]int{}
	for _, m := range got {
		kinds[m.Kind]++
		if m.ContainerId != "abc123" {
			t.Fatalf("container_id = %q", m.ContainerId)
		}
	}
	want := []string{
		KindPrivileged, KindHostNetwork, KindHostPID, KindRootUser,
		KindDangerousCapability, KindDockerSocket, KindPublishedPort,
	}
	for _, k := range want {
		if kinds[k] == 0 {
			t.Errorf("missing kind %s in %#v", k, kinds)
		}
	}
	var foundHighDB bool
	for _, m := range got {
		if m.Kind == KindPublishedPort {
			if m.Detail == "127.0.0.1:8080/tcp" {
				t.Fatal("loopback publish reported")
			}
			if m.Detail == "0.0.0.0:5432/tcp" && m.Severity == "high" {
				foundHighDB = true
			}
		}
	}
	if !foundHighDB {
		t.Fatal("expected high published_port for 5432")
	}
}

func TestFromInspectSkipsCaddyPorts(t *testing.T) {
	d := dockerapi.ContainerDetail{}
	d.Name = "/haetae-caddy"
	d.Config.User = "65532"
	d.HostConfig.PortBindings = map[string][]dockerapi.PortBinding{
		"80/tcp":   {{HostIP: "0.0.0.0", HostPort: "80"}},
		"443/tcp":  {{HostIP: "0.0.0.0", HostPort: "443"}},
		"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}},
	}
	got := FromInspect("c1", d, CaddyPorts{ContainerName: "haetae-caddy", HTTPPort: 80, HTTPSPort: 443})
	for _, m := range got {
		if m.Kind == KindPublishedPort && (m.Detail == "0.0.0.0:80/tcp" || m.Detail == "0.0.0.0:443/tcp") {
			t.Fatalf("caddy port reported: %s", m.Detail)
		}
	}
	found := false
	for _, m := range got {
		if m.Kind == KindPublishedPort && m.Detail == "0.0.0.0:8080/tcp" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected non-caddy published port")
	}
}

func TestCertExpiringProbe(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		notAfter time.Time
		wantSev  string
		wantKind bool
	}{
		{"ok", now.Add(30 * 24 * time.Hour), "", false},
		{"high14", now.Add(10 * 24 * time.Hour), "high", true},
		{"crit3", now.Add(2 * 24 * time.Hour), "critical", true},
		{"expired", now.Add(-time.Hour), "critical", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cert, err := selfSigned(now.Add(-time.Hour), tc.notAfter)
			if err != nil {
				t.Fatal(err)
			}
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{ //nolint:gosec
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					_ = c.(*tls.Conn).Handshake()
					_ = c.Close()
				}
			}()

			opts := Options{
				Now:        func() time.Time { return now },
				TLSTimeout: 2 * time.Second,
			}.withDefaults()
			got := probeCert(context.Background(), opts, "c1", ln.Addr().String(), "localhost")
			if !tc.wantKind {
				if len(got) != 0 {
					t.Fatalf("got %v, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0].Kind != KindCertExpiring || got[0].Severity != tc.wantSev {
				t.Fatalf("got %#v want sev=%s", got, tc.wantSev)
			}
		})
	}
}

func selfSigned(notBefore, notAfter time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
