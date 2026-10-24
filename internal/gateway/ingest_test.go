package gateway

import (
	"context"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeIngest struct {
	Store      // unused methods panic if called
	containers []store.Container
	complete   bool
	host       []store.HostMetric
	ctr        []store.ContainerMetric
}

func (f *fakeIngest) ReplaceContainers(_ context.Context, _, _ uuid.UUID, cs []store.Container, complete bool) error {
	f.containers, f.complete = cs, complete
	return nil
}

func (f *fakeIngest) InsertHostMetrics(_ context.Context, _, _ uuid.UUID, ms []store.HostMetric) error {
	f.host = ms
	return nil
}

func (f *fakeIngest) InsertContainerMetrics(_ context.Context, _, _ uuid.UUID, ms []store.ContainerMetric) error {
	f.ctr = ms
	return nil
}

func newIngestGateway() (*Gateway, *fakeIngest, *session) {
	f := &fakeIngest{}
	g := New(f, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return g, f, newSession(uuid.New(), func(error) {})
}

func TestIngestStateCapsAndTruncation(t *testing.T) {
	g, f, s := newIngestGateway()
	snap := &agentv1.StateSnapshot{DockerReachable: true}
	for range maxContainersPerSnapshot + 10 {
		c := &agentv1.ContainerState{Id: uuid.NewString()}
		for range maxPortsPerContainer + 5 {
			c.Ports = append(c.Ports, &agentv1.PortMapping{PrivatePort: 70000})
		}
		snap.Containers = append(snap.Containers, c)
	}
	g.ingestState(context.Background(), s, snap)

	if len(f.containers) != maxContainersPerSnapshot || f.complete {
		t.Fatalf("stored %d containers complete=%v, want %d and incomplete", len(f.containers), f.complete, maxContainersPerSnapshot)
	}
	if p := f.containers[0].Ports; len(p) != maxPortsPerContainer || p[0].Private != 65535 {
		t.Fatalf("ports not capped/clamped: %d, %v", len(p), p[0])
	}

	f.containers = nil
	g.ingestState(context.Background(), s, &agentv1.StateSnapshot{DockerReachable: false, Containers: snap.Containers[:1]})
	if f.containers != nil {
		t.Fatal("an unreachable-Docker snapshot must not replace the known containers")
	}
}

func TestIngestMetricsDropsBadSamples(t *testing.T) {
	g, f, s := newIngestGateway()
	now := time.Now().UnixMilli()
	g.ingestMetrics(context.Background(), s, &agentv1.MetricsBatch{
		Host: []*agentv1.HostMetricSample{
			{TsUnixMs: now, CpuPct: 12},
			{TsUnixMs: now, CpuPct: math.NaN()},
			{TsUnixMs: now, NetRxBps: math.Inf(1)},
			{TsUnixMs: now - 2*time.Hour.Milliseconds(), CpuPct: 1},
			{TsUnixMs: now, CpuPct: -5, Load1: 1e300},
		},
		Containers: []*agentv1.ContainerMetricSample{
			{ContainerId: "a", TsUnixMs: now, CpuPct: 50},
			{ContainerId: "", TsUnixMs: now},
			{ContainerId: "b", TsUnixMs: now, BlkReadBps: math.NaN()},
		},
	})
	if len(f.host) != 2 {
		t.Fatalf("kept %d host samples, want 2 (NaN, Inf and too old dropped)", len(f.host))
	}
	if f.host[1].CPUPct != 0 || f.host[1].Load1 != math.MaxFloat32 {
		t.Fatalf("out-of-range values not clamped: %+v", f.host[1])
	}
	if len(f.ctr) != 1 || f.ctr[0].ContainerID != "a" {
		t.Fatalf("container samples = %+v", f.ctr)
	}
}
