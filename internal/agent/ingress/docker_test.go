//go:build deploy

package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestDockerIngress(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	netName := "haetae-ing-net-" + suffix
	nginxName := "haetae-ing-nginx-" + suffix
	caddyName := "haetae-ing-caddy-" + suffix
	volPrefix := "haetae-ing-vol-" + suffix

	run := func(c context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(c, "docker", args...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) {
		t.Helper()
		out, err := run(ctx, args...)
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
	}

	// Cleanup must not use the test ctx: defer cancel() runs before t.Cleanup,
	// so a cancelled context would leave containers running.
	cleanup := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		_, _ = run(cctx, "rm", "-f", caddyName)
		_, _ = run(cctx, "rm", "-f", nginxName)
		_, _ = run(cctx, "network", "rm", netName)
		_, _ = run(cctx, "volume", "rm", "-f", volPrefix+"-data")
		_, _ = run(cctx, "volume", "rm", "-f", volPrefix+"-config")
		prefix := "haetae-ing-"
		if left := dockerclean.List(prefix); len(left) > 0 {
			// Only fail on resources from this test's suffix.
			var mine []string
			for _, n := range left {
				if strings.Contains(n, suffix) {
					mine = append(mine, n)
				}
			}
			if len(mine) > 0 {
				t.Errorf("ingress cleanup left resources: %v", mine)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	must("network", "create", netName)
	must("run", "-d", "--name", nginxName, "--network", netName, "nginx:alpine")
	// Wait until nginx serves its welcome page inside the container.
	readyDeadline := time.Now().Add(30 * time.Second)
	for {
		out, err := run(ctx, "exec", nginxName, "wget", "-qO-", "http://127.0.0.1/")
		if err == nil && strings.Contains(out, "Welcome to nginx") {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("nginx not ready: err=%v out=%q", err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}

	httpPort := freePort(t)
	httpsPort := freePort(t)
	stateDir := t.TempDir()

	ing, err := New(Config{
		StateDir:      stateDir,
		HTTPPort:      httpPort,
		HTTPSPort:     httpsPort,
		TLSInternal:   true,
		ContainerName: caddyName,
		VolumePrefix:  volPrefix,
		Runner:        ExecRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()

	url, err := ing.Route(ctx, "app", "app.localtest.me", nginxName+":80")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := fmt.Sprintf("https://app.localtest.me:%d", httpsPort)
	if url != wantURL {
		t.Fatalf("url = %q, want %q", url, wantURL)
	}

	// Wait until the reverse proxy reaches nginx (TLS may succeed before upstream is ready).
	var (
		body   string
		issuer string
	)
	deadline := time.Now().Add(45 * time.Second)
	for {
		body, issuer, err = httpsGet(ctx, "app.localtest.me", httpsPort, "/")
		if err == nil && strings.Contains(body, "Welcome to nginx") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("https get: err=%v body=%q issuer=%q", err, body, issuer)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(issuer, "Caddy Local Authority") {
		t.Fatalf("issuer CN = %q, want containing Caddy Local Authority", issuer)
	}

	// Admin API must not be reachable on TCP 2019 from another container on the
	// same network (bound to a unix socket inside the Caddy container only).
	probeOut, probeErr := run(ctx, "run", "--rm", "--network", netName, "busybox:1.37",
		"wget", "-qO-", "-T", "3", "http://"+caddyName+":2019/config/")
	if probeErr == nil {
		t.Fatalf("admin API reachable on TCP 2019 from peer container: %s", probeOut)
	}

	if err := ing.Unroute(ctx, "app"); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(15 * time.Second)
	var lastErr error
	for {
		_, _, lastErr = httpsGet(ctx, "app.localtest.me", httpsPort, "/")
		if lastErr != nil {
			break
		}
		// Unexpected success: maybe still serving; wait for config to apply.
		if time.Now().After(deadline) {
			t.Fatal("expected request to fail after Unroute")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		t.Fatal("expected error after Unroute")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func httpsGet(ctx context.Context, host string, port int, path string) (body, issuerCN string, err error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var peerCerts []*x509.Certificate
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // Caddy internal CA
			ServerName:         host,
			VerifyConnection: func(cs tls.ConnectionState) error {
				peerCerts = cs.PeerCertificates
				return nil
			},
		},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if len(peerCerts) > 0 {
		issuerCN = peerCerts[0].Issuer.CommonName
	}
	if resp.StatusCode != http.StatusOK {
		return string(b), issuerCN, fmt.Errorf("status %d", resp.StatusCode)
	}
	return string(b), issuerCN, nil
}
