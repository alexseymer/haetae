package gateway

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

// Limits on what one agent message may carry. A misbehaving agent cannot make
// the server store unbounded amounts of data.
const (
	maxContainersPerSnapshot = 500
	maxPortsPerContainer     = 64
	maxHostSamplesPerBatch   = 100
	maxContainerSamples      = 5000
	// Samples outside this window are dropped: they usually mean a broken
	// clock, and older ones would miss the rollups (which look back 2 h).
	// The agent buffers at most a few minutes anyway.
	maxSampleAge    = time.Hour
	maxSampleFuture = 5 * time.Minute
)

func (g *Gateway) ingestState(ctx context.Context, s *session, snap *agentv1.StateSnapshot) {
	cs := snap.GetContainers()
	complete := true
	if len(cs) > maxContainersPerSnapshot {
		g.log.Warn("state snapshot truncated", "host_id", s.hostID, "containers", len(cs))
		cs = cs[:maxContainersPerSnapshot]
		// Containers past the cap are unknown, not removed.
		complete = false
	}
	if !snap.GetDockerReachable() {
		// Keep the last known containers rather than marking all removed;
		// the host view shows Docker as unreachable instead.
		g.log.Warn("docker unreachable on host", "host_id", s.hostID, "err", clip(snap.GetDockerError()))
		return
	}

	out := make([]store.Container, 0, len(cs))
	for _, c := range cs {
		if c.GetId() == "" {
			continue
		}
		out = append(out, toContainer(c))
	}
	if err := g.store.ReplaceContainers(ctx, s.orgID, s.hostID, out, complete); err != nil {
		g.log.Error("store containers", "host_id", s.hostID, "err", err)
	}
}

func toContainer(c *agentv1.ContainerState) store.Container {
	ports := c.GetPorts()
	if len(ports) > maxPortsPerContainer {
		ports = ports[:maxPortsPerContainer]
	}
	sp := make([]store.Port, 0, len(ports))
	for _, p := range ports {
		sp = append(sp, store.Port{
			IP:      clip(p.GetIp()),
			Private: uint16(min(p.GetPrivatePort(), 65535)), //nolint:gosec // G115: clamped
			Public:  uint16(min(p.GetPublicPort(), 65535)),  //nolint:gosec // G115: clamped
			Proto:   clip(p.GetProto()),
		})
	}
	return store.Container{
		ContainerID:  clip(c.GetId()),
		Name:         clip(c.GetName()),
		Stack:        clip(c.GetStack()),
		Service:      clip(c.GetService()),
		Image:        clip(c.GetImage()),
		ImageVersion: clip(c.GetImageVersion()),
		State:        clip(c.GetState()),
		Status:       clip(c.GetStatus()),
		Health:       clip(c.GetHealth()),
		RestartCount: int(c.GetRestartCount()),
		OOMKilled:    c.GetOomKilled(),
		ExitCode:     int(c.GetExitCode()),
		Ports:        sp,
		CreatedAt:    unixMs(c.GetCreatedUnixMs()),
		StartedAt:    unixMs(c.GetStartedUnixMs()),
	}
}

func unixMs(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

func sampleTimeOK(ts, now time.Time) bool {
	return ts.After(now.Add(-maxSampleAge)) && ts.Before(now.Add(maxSampleFuture))
}

func (g *Gateway) ingestMetrics(ctx context.Context, s *session, b *agentv1.MetricsBatch) {
	now := time.Now()

	hs := b.GetHost()
	if len(hs) > maxHostSamplesPerBatch {
		hs = hs[:maxHostSamplesPerBatch]
	}
	host := make([]store.HostMetric, 0, len(hs))
	for _, h := range hs {
		ts := time.UnixMilli(h.GetTsUnixMs()).UTC()
		if !sampleTimeOK(ts, now) || !finite(h.GetCpuPct(), h.GetLoad1(), h.GetNetRxBps(), h.GetNetTxBps()) {
			continue
		}
		host = append(host, store.HostMetric{
			TS:        ts,
			CPUPct:    gauge(h.GetCpuPct()),
			Load1:     gauge(h.GetLoad1()),
			MemTotal:  toInt64(h.GetMemTotal()),
			MemAvail:  toInt64(h.GetMemAvail()),
			DiskTotal: toInt64(h.GetDiskTotal()),
			DiskUsed:  toInt64(h.GetDiskUsed()),
			NetRxBps:  gauge(h.GetNetRxBps()),
			NetTxBps:  gauge(h.GetNetTxBps()),
		})
	}

	cs := b.GetContainers()
	if len(cs) > maxContainerSamples {
		cs = cs[:maxContainerSamples]
	}
	ctr := make([]store.ContainerMetric, 0, len(cs))
	for _, c := range cs {
		ts := time.UnixMilli(c.GetTsUnixMs()).UTC()
		if c.GetContainerId() == "" || !sampleTimeOK(ts, now) ||
			!finite(c.GetCpuPct(), c.GetNetRxBps(), c.GetNetTxBps(), c.GetBlkReadBps(), c.GetBlkWriteBps()) {
			continue
		}
		ctr = append(ctr, store.ContainerMetric{
			ContainerID: clip(c.GetContainerId()),
			TS:          ts,
			CPUPct:      gauge(c.GetCpuPct()),
			MemUsage:    toInt64(c.GetMemUsage()),
			MemLimit:    toInt64(c.GetMemLimit()),
			NetRxBps:    gauge(c.GetNetRxBps()),
			NetTxBps:    gauge(c.GetNetTxBps()),
			BlkReadBps:  gauge(c.GetBlkReadBps()),
			BlkWriteBps: gauge(c.GetBlkWriteBps()),
			Pids:        int(c.GetPids()),
		})
	}

	if len(host) > 0 {
		if err := g.store.InsertHostMetrics(ctx, s.orgID, s.hostID, host); err != nil {
			g.log.Error("store host metrics", "host_id", s.hostID, "err", err)
		}
	}
	if len(ctr) > 0 {
		if err := g.store.InsertContainerMetrics(ctx, s.orgID, s.hostID, ctr); err != nil {
			g.log.Error("store container metrics", "host_id", s.hostID, "err", err)
		}
	}
}

// finite reports whether no value is NaN or infinite. One such value would
// turn a whole rollup bucket into NaN.
func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// gauge clamps a value into what a PostgreSQL real column can hold; metrics
// are never negative. An out-of-range value would fail the whole batch.
func gauge(v float64) float64 {
	return min(max(v, 0), math.MaxFloat32)
}

func toInt64(v uint64) int64 {
	if v > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(v)
}

// ingestStore is the part of Store used for state and metrics; kept separate
// so its methods are easy to find.
type ingestStore interface {
	ReplaceContainers(ctx context.Context, orgID, hostID uuid.UUID, cs []store.Container, complete bool) error
	InsertHostMetrics(ctx context.Context, orgID, hostID uuid.UUID, ms []store.HostMetric) error
	InsertContainerMetrics(ctx context.Context, orgID, hostID uuid.UUID, ms []store.ContainerMetric) error
}
