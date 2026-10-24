package agent

import (
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
)

func stats(at time.Time, cpuTotal, cpuSys, rx, tx, rd, wr uint64) dockerapi.Stats {
	var s dockerapi.Stats
	s.Read = at
	s.CPUStats.CPUUsage.TotalUsage = cpuTotal
	s.CPUStats.SystemUsage = cpuSys
	s.CPUStats.OnlineCPUs = 2
	s.MemoryStats.Usage = 300
	s.MemoryStats.Limit = 1000
	s.MemoryStats.Stats = map[string]uint64{"inactive_file": 100}
	s.Networks = map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	}{"eth0": {RxBytes: rx, TxBytes: tx}}
	s.BlkioStats.IoServiceBytesRecursive = []struct {
		Op    string `json:"op"`
		Value uint64 `json:"value"`
	}{{Op: "read", Value: rd}, {Op: "Write", Value: wr}}
	return s
}

func TestContainerSampleRates(t *testing.T) {
	m := NewMonitor(MonitorConfig{}, NewOutbox(1), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t0 := time.Unix(1000, 0)

	if s := m.containerSample("c1", stats(t0, 1000, 10000, 0, 0, 0, 0)); s != nil {
		t.Fatalf("first reading should only set a baseline, got %v", s)
	}
	s := m.containerSample("c1", stats(t0.Add(10*time.Second), 1500, 12000, 1000, 2000, 500, 5000))
	if s == nil {
		t.Fatal("second reading returned nil")
	}
	// docker stats formula: 500/2000 * 2 CPUs * 100 = 50% (half a core)
	if math.Abs(s.GetCpuPct()-50) > 1e-9 {
		t.Errorf("cpu = %v, want 50", s.GetCpuPct())
	}
	if s.GetMemUsage() != 200 || s.GetMemLimit() != 1000 {
		t.Errorf("mem = %d/%d, want 200/1000 (cache excluded)", s.GetMemUsage(), s.GetMemLimit())
	}
	if s.GetNetRxBps() != 100 || s.GetNetTxBps() != 200 || s.GetBlkReadBps() != 50 || s.GetBlkWriteBps() != 500 {
		t.Errorf("rates rx=%v tx=%v rd=%v wr=%v", s.GetNetRxBps(), s.GetNetTxBps(), s.GetBlkReadBps(), s.GetBlkWriteBps())
	}

	// Counters going backwards (container restarted) give zero rates, not huge ones.
	s = m.containerSample("c1", stats(t0.Add(20*time.Second), 100, 13000, 10, 10, 0, 0))
	if s.GetNetRxBps() != 0 || s.GetCpuPct() != 0 {
		t.Errorf("after reset: rx=%v cpu=%v, want 0", s.GetNetRxBps(), s.GetCpuPct())
	}
}

func TestRelevantEvent(t *testing.T) {
	for action, want := range map[string]bool{
		"start": true, "die": true, "destroy": true, "oom": true,
		"health_status: unhealthy": true, "exec_start: sh": false, "attach": false,
	} {
		if got := relevantEvent(action); got != want {
			t.Errorf("relevantEvent(%q) = %v, want %v", action, got, want)
		}
	}
}
