package imagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":          true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"10.1.2.3":         false,
		"172.17.0.1":       false,
		"192.168.8.110":    false,
		"169.254.169.254":  false, // cloud metadata
		"100.100.100.200":  false, // CGNAT range, also used for metadata
		"0.0.0.0":          false,
		"::1":              false,
		"fd00::1":          false,
		"fe80::1":          false,
		"::ffff:127.0.0.1": false,
		"0.0.0.1":          false, // Linux routes 0.0.0.0/8 to localhost
		"240.0.0.1":        false,
		"203.0.113.9":      false,
		"64:ff9b::7f00:1":  false, // NAT64 form of 127.0.0.1
		"2002:7f00:1::":    false, // 6to4 form of 127.0.0.1
		"2001::1":          false, // Teredo
	} {
		if got := isPublic(netip.MustParseAddr(addr)); got != want {
			t.Errorf("isPublic(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestPrivateRegistry(t *testing.T) {
	ctx := context.Background()
	for host, want := range map[string]bool{
		"127.0.0.1:5000": true,
		"localhost:5000": true,
		"[::1]:5000":     true,
		"10.0.0.5":       true,
		"8.8.8.8":        false,
	} {
		if got := privateRegistry(ctx, host) != nil; got != want {
			t.Errorf("privateRegistry(%s) allowance = %v, want %v", host, got, want)
		}
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln
}

func TestGuardedDialAllowance(t *testing.T) {
	ln := listen(t)
	ctx := context.Background()
	port := portOf(ln)
	loop := netip.MustParseAddr("127.0.0.1")

	// No allowance (a public registry): loopback is refused, by IP and by name.
	if _, err := guardedDial(ctx, "tcp", ln.Addr().String()); !errors.Is(err, errBlockedAddr) {
		t.Fatalf("no allowance, loopback IP: want blocked, got %v", err)
	}
	if _, err := guardedDial(ctx, "tcp", "localhost:"+port); !errors.Is(err, errBlockedAddr) {
		t.Fatalf("no allowance, hostname resolving to loopback: want blocked, got %v", err)
	}

	// A self-hosted registry on loopback may be dialled, and only at the
	// resolved address.
	allow := &privateAllowance{host: "127.0.0.1", addrs: []netip.Addr{loop}}
	actx := withPrivateAllowance(ctx, allow)
	c, err := guardedDial(actx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("registry host with allowance: %v", err)
	}
	_ = c.Close()

	// The allowance is for that host only: a token realm or redirect target
	// elsewhere on private space stays blocked, whatever name it uses.
	for _, target := range []string{"localhost:" + port, "[::1]:" + port, "169.254.169.254:80", "10.1.2.3:443"} {
		if _, err := guardedDial(actx, "tcp", target); !errors.Is(err, errBlockedAddr) {
			t.Errorf("allowance for 127.0.0.1 must not cover %s, got %v", target, err)
		}
	}

	// DNS rebinding: the allowance pins the addresses seen at lookup time. A
	// host that now resolves somewhere else (or to a different private
	// address) is not reachable through it.
	pinned := &privateAllowance{host: "localhost", addrs: []netip.Addr{netip.MustParseAddr("10.9.9.9")}}
	if _, err := guardedDial(withPrivateAllowance(ctx, pinned), "tcp", "localhost:"+port); !errors.Is(err, errBlockedAddr) {
		t.Fatalf("address outside the pinned set: want blocked, got %v", err)
	}
}

// A public HTTPS registry must not be able to bounce the agent to the local
// Docker API (or anything else on loopback) with a redirect.
func TestRegistryRedirectToLoopbackRefused(t *testing.T) {
	var hit bool
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer local.Close()

	cases := []struct {
		name string
		from string
		to   string
		ok   bool
	}{
		{"https to http loopback", "https://registry.example.com/v2/", local.URL + "/containers/json", false},
		{"https to http public", "https://registry.example.com/v2/", "http://cdn.example.com/blob", false},
		{"https to https", "https://registry.example.com/v2/", "https://cdn.example.com/blob", true},
		{"http loopback to http loopback", "http://127.0.0.1:5000/v2/", "http://127.0.0.1:5000/v2/x", true},
		{"http loopback to http public", "http://127.0.0.1:5000/v2/", "http://cdn.example.com/blob", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, _ := url.Parse(c.from)
			to, _ := url.Parse(c.to)
			err := checkRedirect(&http.Request{URL: to}, []*http.Request{{URL: from}})
			if (err == nil) != c.ok {
				t.Fatalf("checkRedirect err = %v, want ok=%v", err, c.ok)
			}
		})
	}

	// End to end through the production client: the token realm points at
	// loopback while the registry is public; the guarded dialer refuses it.
	c := &RegistryClient{Timeout: 2 * time.Second}
	ctx := context.Background()
	_, err := c.bearerToken(ctx, `Bearer realm="https://`+strings.TrimPrefix(local.URL, "http://")+`/token"`, "library/x")
	if err == nil || !errors.Is(err, errBlockedAddr) {
		t.Fatalf("realm on loopback: want blocked, got %v", err)
	}
	if hit {
		t.Fatal("local server was contacted")
	}
}

func TestPullRefusesUnknownCreationTime(t *testing.T) {
	configJSON, _ := json.Marshal(map[string]string{"architecture": "amd64"}) // no "created"
	configDigest := "sha256:" + sha256hex(configJSON)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[]}`, configDigest))
	manifestDigest := "sha256:" + sha256hex(manifest)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/library/app/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", manifestDigest)
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/v2/library/app/blobs/"+configDigest, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(configJSON)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	chk := &Checker{
		Registry: &RegistryClient{HTTP: srv.Client(), Timeout: time.Second},
		Now:      time.Now,
	}
	err := chk.pullHost(context.Background(), strings.TrimPrefix(srv.URL, "https://"), "library/app", "latest",
		&agentv1.PullImage{Ref: "app:latest", Digest: manifestDigest, MinAgeHours: 48})
	if err == nil || !strings.Contains(err.Error(), "creation time unknown") {
		t.Fatalf("want unknown creation time refusal, got %v", err)
	}
}

func portOf(ln net.Listener) string {
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	return p
}
