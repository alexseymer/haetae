//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/gateway"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker not available: %v", err)
	}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// recorder keeps the wire bytes of every message agents send.
type recorder struct {
	mu   sync.Mutex
	wire [][]byte
}

func (r *recorder) tap(_ uuid.UUID, msg *agentv1.ConnectRequest) {
	b, err := proto.Marshal(msg)
	if err != nil {
		panic(err)
	}
	r.mu.Lock()
	r.wire = append(r.wire, b)
	r.mu.Unlock()
}

func (r *recorder) contains(needle []byte) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.wire {
		if bytes.Contains(b, needle) {
			return len(r.wire), true
		}
	}
	return len(r.wire), false
}

// S1 (metrics within 2 minutes) and M2: state and metrics of a real Docker
// host reach the control plane; env values, command lines and arbitrary
// labels never leave the host.
func TestStateAndMetricsReachServer(t *testing.T) {
	requireDocker(t)
	rec := &recorder{}
	e := newEnvWith(t, func(c *gateway.Config) { c.Tap = rec.tap })

	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	canary := "canary" + hex.EncodeToString(raw)
	name := "haetae-it-" + hex.EncodeToString(raw[:4])
	docker(t, "pull", "-q", "busybox:1.37")
	cid := docker(t, "run", "-d", "--rm", "--name", name,
		"--label", "com.docker.compose.project=itstack",
		"--label", "com.docker.compose.service=web",
		"--label", "com.example.secret="+canary,
		"-e", "HAETAE_CANARY="+canary,
		"-p", "127.0.0.1::8080",
		"busybox:1.37", "sh", "-c", "while true; do sleep 1; done # "+canary)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", cid).Run() })

	start := time.Now()
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("metrics-host"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := agent.NewOutbox(30)
	mon := agent.NewMonitor(agent.MonitorConfig{}, out, e.log.With("component", "monitor")) // production intervals
	e.runAgentWith(id, agent.RunOptions{Outbox: out, OnConnected: mon.Resync, DockerVersion: mon.DockerVersion}, mon)

	hostID := uuid.MustParse(id.HostID)
	ctx := context.Background()

	var got store.Container
	waitFor(t, 30*time.Second, "container in server state", func() bool {
		cs, err := e.st.ListContainers(ctx, e.org.ID, hostID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if c.ContainerID == cid {
				got = c
				return true
			}
		}
		return false
	})
	t.Logf("MEASURE M2 container state visible after: %s", time.Since(start).Round(time.Millisecond))
	if got.Stack != "itstack" || got.Service != "web" || got.State != "running" || got.Name != name {
		t.Errorf("container = %+v", got)
	}
	if len(got.Ports) != 1 || got.Ports[0].Private != 8080 || got.Ports[0].Public == 0 {
		t.Errorf("ports = %+v, want 8080 published", got.Ports)
	}
	if h := e.host(id.HostID); h.Info.DockerVersion == "" {
		t.Errorf("docker version not reported")
	}

	waitFor(t, 2*time.Minute, "host metrics queryable", func() bool {
		_, pts, err := e.st.HostSeries(ctx, e.org.ID, hostID, "mem_used", start.Add(-time.Minute), time.Now().Add(time.Minute))
		return err == nil && len(pts) > 0
	})
	hostAfter := time.Since(start)
	waitFor(t, 2*time.Minute-hostAfter, "container metrics queryable", func() bool {
		_, pts, err := e.st.ContainerSeries(ctx, e.org.ID, hostID, cid, "mem_usage", start.Add(-time.Minute), time.Now().Add(time.Minute))
		return err == nil && len(pts) > 0
	})
	t.Logf("MEASURE S1 enrollment-to-host-metrics: %s, to-container-metrics: %s (limit 2m)",
		hostAfter.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))

	// A lifecycle event reaches the server without waiting for the 60 s refresh.
	stopped := time.Now()
	docker(t, "rm", "-f", cid)
	waitFor(t, 20*time.Second, "removed container leaves server state", func() bool {
		cs, err := e.st.ListContainers(ctx, e.org.ID, hostID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if c.ContainerID == cid {
				return false
			}
		}
		return true
	})
	t.Logf("MEASURE M2 container removal visible after: %s", time.Since(stopped).Round(time.Millisecond))

	n, leaked := rec.contains([]byte(canary))
	if n == 0 {
		t.Fatal("recorder saw no messages; the tap is broken")
	}
	if leaked {
		t.Fatalf("canary from env, label or command line reached the server (%d messages checked)", n)
	}
	t.Logf("checked %d agent messages: no env value, command line or extra label reached the server", n)
}
