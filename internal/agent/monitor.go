package agent

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	"github.com/alexseymer/haetae/internal/agent/hoststats"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// MonitorConfig tunes the monitor. Zero values get defaults.
type MonitorConfig struct {
	DockerSocket  string        // default /var/run/docker.sock
	ProcRoot      string        // default /proc
	DiskPath      string        // filesystem to report, default /
	Interval      time.Duration // metrics sampling, default 10 s
	SnapshotEvery time.Duration // full state refresh, default 60 s
}

func (c MonitorConfig) withDefaults() MonitorConfig {
	if c.DockerSocket == "" {
		c.DockerSocket = "/var/run/docker.sock"
	}
	if c.ProcRoot == "" {
		c.ProcRoot = "/proc"
	}
	if c.DiskPath == "" {
		c.DiskPath = "/"
	}
	if c.Interval <= 0 {
		c.Interval = 10 * time.Second
	}
	if c.SnapshotEvery <= 0 {
		c.SnapshotEvery = time.Minute
	}
	return c
}

// Monitor samples Docker state and host and container metrics into an Outbox.
type Monitor struct {
	cfg    MonitorConfig
	docker *dockerapi.Client
	host   *hoststats.Collector
	out    *Outbox
	log    *slog.Logger
	resync chan struct{}

	// Previous container counters, keyed by container id; only containers
	// seen in the last sample are kept, so the map stays bounded.
	prev    map[string]containerCounters
	running []string // ids of running containers from the last snapshot
	rotate  int      // rotates the starting container of each stats round
}

type containerCounters struct {
	at                  time.Time
	cpuTotal, cpuSystem uint64
	netRx, netTx        uint64
	blkRead, blkWrite   uint64
}

// NewMonitor returns a monitor writing to out.
func NewMonitor(cfg MonitorConfig, out *Outbox, log *slog.Logger) *Monitor {
	cfg = cfg.withDefaults()
	return &Monitor{
		cfg:    cfg,
		docker: dockerapi.New(cfg.DockerSocket),
		host:   hoststats.NewCollector(cfg.ProcRoot, cfg.DiskPath),
		out:    out,
		log:    log,
		resync: make(chan struct{}, 1),
		prev:   make(map[string]containerCounters),
	}
}

// Resync asks for a fresh state snapshot, e.g. after reconnecting.
func (m *Monitor) Resync() {
	select {
	case m.resync <- struct{}{}:
	default:
	}
}

// DockerVersion returns the daemon version, or "" if Docker is unreachable.
func (m *Monitor) DockerVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v, err := m.docker.Version(ctx)
	if err != nil {
		return ""
	}
	return v.Version
}

// Run samples until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	go m.watchEvents(ctx)

	m.snapshot(ctx)
	metrics := time.NewTicker(m.cfg.Interval)
	defer metrics.Stop()
	refresh := time.NewTicker(m.cfg.SnapshotEvery)
	defer refresh.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-metrics.C:
			m.sample(ctx)
		case <-refresh.C:
			m.snapshot(ctx)
		case <-m.resync:
			// Coalesce bursts of events into one snapshot.
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			select {
			case <-m.resync:
			default:
			}
			m.snapshot(ctx)
		}
	}
}

// watchEvents triggers a snapshot on container lifecycle events and
// reconnects to the event stream with backoff.
func (m *Monitor) watchEvents(ctx context.Context) {
	backoff := time.Second
	for first := true; ctx.Err() == nil; first = false {
		started := time.Now()
		// Events missed while the stream was down are not replayed; a
		// snapshot catches up instead.
		if !first {
			m.Resync()
		}
		err := m.docker.Events(ctx, func(ev dockerapi.Event) {
			if relevantEvent(ev.Action) {
				m.Resync()
			}
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		m.log.Debug("docker event stream ended", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func relevantEvent(action string) bool {
	if strings.HasPrefix(action, "health_status") {
		return true
	}
	switch action {
	case "create", "start", "restart", "die", "stop", "kill", "oom", "destroy", "pause", "unpause", "rename", "update":
		return true
	}
	return false
}

func (m *Monitor) snapshot(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	snap := &agentv1.StateSnapshot{TakenUnixMs: time.Now().UnixMilli()}
	ver, err := m.docker.Version(ctx)
	if err != nil {
		snap.DockerError = err.Error()
		m.running = nil
		m.out.PutSnapshot(snap)
		return
	}
	snap.DockerReachable = true
	snap.DockerVersion = ver.Version

	list, err := m.docker.ListContainers(ctx, true)
	if err != nil {
		snap.DockerReachable = false
		snap.DockerError = err.Error()
		m.out.PutSnapshot(snap)
		return
	}

	running := make([]string, 0, len(list))
	for _, c := range list {
		cs := containerState(c)
		// Inspect adds restart count, health, OOM and exit code. A container
		// can vanish between list and inspect; keep what the list gave us.
		if d, err := m.docker.InspectContainer(ctx, c.ID); err == nil {
			addDetail(cs, d)
		}
		snap.Containers = append(snap.Containers, cs)
		if c.State == "running" {
			running = append(running, c.ID)
		}
	}
	m.running = running
	m.out.PutSnapshot(snap)
}

func containerState(c dockerapi.ContainerSummary) *agentv1.ContainerState {
	name := c.ID
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	cs := &agentv1.ContainerState{
		Id:            c.ID,
		Name:          name,
		Stack:         c.Labels.ComposeProject,
		Service:       c.Labels.ComposeService,
		Image:         c.Image,
		ImageVersion:  c.Labels.ImageVersion,
		State:         c.State,
		Status:        c.Status,
		CreatedUnixMs: c.Created * 1000,
	}
	for _, p := range c.Ports {
		cs.Ports = append(cs.Ports, &agentv1.PortMapping{
			Ip:          p.IP,
			PrivatePort: uint32(p.PrivatePort),
			PublicPort:  uint32(p.PublicPort),
			Proto:       p.Type,
		})
	}
	return cs
}

func addDetail(cs *agentv1.ContainerState, d dockerapi.ContainerDetail) {
	cs.RestartCount = int32(min(d.RestartCount, 1<<31-1)) //nolint:gosec // G115: clamped
	cs.OomKilled = d.State.OOMKilled
	cs.ExitCode = int32(d.State.ExitCode) //nolint:gosec // G115: Docker exit codes fit in int32
	if d.State.Health != nil {
		cs.Health = d.State.Health.Status
	}
	if t, err := time.Parse(time.RFC3339Nano, d.State.StartedAt); err == nil && !t.IsZero() && t.Year() > 1 {
		cs.StartedUnixMs = t.UnixMilli()
	}
}

func (m *Monitor) sample(ctx context.Context) {
	batch := &agentv1.MetricsBatch{}

	if h, err := m.host.Sample(); err == nil {
		batch.Host = append(batch.Host, &agentv1.HostMetricSample{
			TsUnixMs:  h.At.UnixMilli(),
			CpuPct:    h.CPUPercent,
			Load1:     h.Load1,
			MemTotal:  h.MemTotal,
			MemAvail:  h.MemAvailable,
			DiskTotal: h.DiskTotal,
			DiskUsed:  h.DiskUsed,
			NetRxBps:  h.NetRxBps,
			NetTxBps:  h.NetTxBps,
		})
	} else {
		m.log.Debug("host sample failed", "err", err)
	}

	// The whole round shares one deadline so a slow dockerd cannot hold up
	// Run (and event-triggered snapshots) for longer than one interval.
	// Containers not reached this round are sampled next time.
	round, cancel := context.WithTimeout(ctx, m.cfg.Interval)
	defer cancel()
	seen := make(map[string]bool, len(m.running))
	// Start at a different container each round, so one that always hangs
	// cannot starve the ones after it.
	start := 0
	if n := len(m.running); n > 0 {
		start = m.rotate % n
		m.rotate++
	}
	for i := range m.running {
		id := m.running[(start+i)%len(m.running)]
		if round.Err() != nil {
			// Keep baselines of skipped containers.
			seen[id] = true
			continue
		}
		cctx, ccancel := context.WithTimeout(round, 2*time.Second)
		st, err := m.docker.Stats(cctx, id)
		ccancel()
		if err != nil {
			seen[id] = true // transient; keep its baseline
			continue
		}
		seen[id] = true
		if s := m.containerSample(id, st); s != nil {
			batch.Containers = append(batch.Containers, s)
		}
	}
	for id := range m.prev {
		if !seen[id] {
			delete(m.prev, id)
		}
	}

	if len(batch.Host) > 0 || len(batch.Containers) > 0 {
		m.out.PutMetrics(batch)
	}
}

// containerSample turns a stats reading into a sample with rates. The first
// reading of a container only records a baseline and returns nil.
func (m *Monitor) containerSample(id string, st dockerapi.Stats) *agentv1.ContainerMetricSample {
	at := st.Read
	if at.IsZero() {
		at = time.Now()
	}
	cur := containerCounters{
		at:        at,
		cpuTotal:  st.CPUStats.CPUUsage.TotalUsage,
		cpuSystem: st.CPUStats.SystemUsage,
	}
	for _, n := range st.Networks {
		cur.netRx += n.RxBytes
		cur.netTx += n.TxBytes
	}
	for _, b := range st.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			cur.blkRead += b.Value
		case "write":
			cur.blkWrite += b.Value
		}
	}

	prev, ok := m.prev[id]
	m.prev[id] = cur
	if !ok {
		return nil
	}
	secs := cur.at.Sub(prev.at).Seconds()
	if secs <= 0 {
		return nil
	}

	cpus := float64(max(st.CPUStats.OnlineCPUs, 1))
	cpu := 0.0
	if dSys := delta(cur.cpuSystem, prev.cpuSystem); dSys > 0 {
		// Same formula as `docker stats`: 100 means one full core.
		cpu = float64(delta(cur.cpuTotal, prev.cpuTotal)) / float64(dSys) * cpus * 100
	}

	return &agentv1.ContainerMetricSample{
		ContainerId: id,
		TsUnixMs:    at.UnixMilli(),
		CpuPct:      cpu,
		MemUsage:    memUsage(st),
		MemLimit:    st.MemoryStats.Limit,
		NetRxBps:    float64(delta(cur.netRx, prev.netRx)) / secs,
		NetTxBps:    float64(delta(cur.netTx, prev.netTx)) / secs,
		BlkReadBps:  float64(delta(cur.blkRead, prev.blkRead)) / secs,
		BlkWriteBps: float64(delta(cur.blkWrite, prev.blkWrite)) / secs,
		Pids:        uint32(min(st.PidsStats.Current, 1<<32-1)), //nolint:gosec // G115: clamped
	}
}

// delta returns cur-prev, or 0 when a counter went backwards (restart).
func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// memUsage excludes reclaimable page cache, as `docker stats` does.
func memUsage(st dockerapi.Stats) uint64 {
	usage := st.MemoryStats.Usage
	cache, ok := st.MemoryStats.Stats["inactive_file"] // cgroup v2
	if !ok {
		cache = st.MemoryStats.Stats["total_inactive_file"] // cgroup v1
	}
	if cache < usage {
		return usage - cache
	}
	return usage
}
