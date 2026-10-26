package conntrack

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAvailableWithoutProcfsEntry(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"sysctl only (no CONFIG_NF_CONNTRACK_PROCFS)", []string{"sys/net/netfilter/nf_conntrack_max"}, true},
		{"procfs entry only", []string{"net/nf_conntrack"}, true},
		{"module not loaded", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range c.files {
				p := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("1"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ok, reason := available(root)
			if ok != c.want {
				t.Fatalf("available = %v (%q), want %v", ok, reason, c.want)
			}
			if !ok && reason != "no nf_conntrack" {
				t.Fatalf("reason = %q", reason)
			}
		})
	}
}

// TestKernelSourceRealEvent needs root and a loaded nf_conntrack; it opens a
// TCP connection and expects the matching NEW event from netlink.
func TestKernelSourceRealEvent(t *testing.T) {
	if ok, reason := Available(); !ok {
		t.Skip(reason)
	}
	if os.Geteuid() != 0 {
		t.Skip("needs CAP_NET_ADMIN")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan Event, 64)
	done := make(chan error, 1)
	go func() {
		done <- KernelSource{}.Run(ctx, func(e Event) {
			select {
			case got <- e:
			default:
			}
		})
	}()
	time.Sleep(300 * time.Millisecond) // let the multicast subscription settle
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	for {
		select {
		case e := <-got:
			if e.Proto == "tcp" && e.OrigDstPort == port {
				t.Logf("event: %+v", e)
				return
			}
		case err := <-done:
			t.Fatalf("source stopped: %v", err)
		case <-ctx.Done():
			t.Fatal("no conntrack NEW event for the test connection")
		}
	}
}
