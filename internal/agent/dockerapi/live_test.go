//go:build integration

package dockerapi

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestLiveVersionAndList(t *testing.T) {
	const sock = "/var/run/docker.sock"
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Skipf("docker socket not accessible: %v", err)
	}
	_ = conn.Close()

	if _, err := os.Stat(sock); err != nil {
		t.Skipf("docker socket not accessible: %v", err)
	}

	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	v, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v.Version == "" {
		t.Fatal("empty Version")
	}

	list, err := c.ListContainers(ctx, true)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	_ = list // may be empty; success is enough
}
