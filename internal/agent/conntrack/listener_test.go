package conntrack

import (
	"context"
	"net/netip"
	"runtime"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

type memEmit struct {
	mu      sync.Mutex
	batches []*agentv1.OutboundBatch
}

func (m *memEmit) PutOutboundBatch(b *agentv1.OutboundBatch) {
	m.mu.Lock()
	m.batches = append(m.batches, b)
	m.mu.Unlock()
}

func (m *memEmit) all() []*agentv1.OutboundBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*agentv1.OutboundBatch(nil), m.batches...)
}

func TestAttributionAndAggregation(t *testing.T) {
	events := make(chan Event, 100)
	emit := &memEmit{}
	l := NewListener(nil, emit, nil)
	l.Source = FakeSource{Events: events}
	l.Window = 50 * time.Millisecond
	bridge := netip.MustParsePrefix("172.18.0.0/16")
	l.SetInventoryForTest(map[string]string{
		"172.18.0.2": "ctr-a",
		"172.18.0.3": "ctr-b",
	}, []netip.Prefix{bridge}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	srcA := netip.MustParseAddr("172.18.0.2")
	srcB := netip.MustParseAddr("172.18.0.3")
	dst := netip.MustParseAddr("1.2.3.4")
	events <- Event{Proto: "tcp", OrigSrc: srcA, OrigDst: dst, OrigDstPort: 443}
	events <- Event{Proto: "tcp", OrigSrc: srcA, OrigDst: dst, OrigDstPort: 443}
	events <- Event{Proto: "tcp", OrigSrc: srcB, OrigDst: dst, OrigDstPort: 80}
	// container-to-container — dropped
	events <- Event{Proto: "tcp", OrigSrc: srcA, OrigDst: netip.MustParseAddr("172.18.0.3"), OrigDstPort: 8080}
	// loopback — dropped
	events <- Event{Proto: "tcp", OrigSrc: srcA, OrigDst: netip.MustParseAddr("127.0.0.1"), OrigDstPort: 80}

	waitBatches(t, emit, 1, 2*time.Second)
	cancel()

	batches := emit.all()
	if len(batches) == 0 {
		t.Fatal("no batches")
	}
	b := batches[0]
	if b.Dropped != 0 {
		t.Fatalf("dropped=%d", b.Dropped)
	}
	by := map[string]uint32{}
	for _, c := range b.Conns {
		by[c.ContainerId+":"+c.DstIp+":"+itoa(c.DstPort)] = c.Count
	}
	if by["ctr-a:1.2.3.4:443"] != 2 {
		t.Fatalf("ctr-a count = %v", by)
	}
	if by["ctr-b:1.2.3.4:80"] != 1 {
		t.Fatalf("ctr-b count = %v", by)
	}
	if len(b.Conns) != 2 {
		t.Fatalf("conns=%d want 2 (c2c/loopback dropped)", len(b.Conns))
	}
}

func TestCapAndDropped(t *testing.T) {
	events := make(chan Event, 500)
	emit := &memEmit{}
	l := NewListener(nil, emit, nil)
	l.Source = FakeSource{Events: events}
	l.Window = 80 * time.Millisecond
	l.SetInventoryForTest(map[string]string{"172.18.0.2": "ctr"}, []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	src := netip.MustParseAddr("172.18.0.2")
	for i := 0; i < maxDestinations+50; i++ {
		events <- Event{
			Proto: "tcp", OrigSrc: src,
			OrigDst:     netip.AddrFrom4([4]byte{8, 8, byte(i >> 8), byte(i)}),
			OrigDstPort: uint16(i + 1),
		}
	}
	waitBatches(t, emit, 1, 3*time.Second)
	cancel()
	b := emit.all()[0]
	if len(b.Conns) > maxDestinations {
		t.Fatalf("conns=%d > cap", len(b.Conns))
	}
	if b.Dropped == 0 {
		t.Fatal("expected dropped > 0")
	}
}

func TestHostNetworkNotMonitored(t *testing.T) {
	events := make(chan Event, 10)
	emit := &memEmit{}
	l := NewListener(nil, emit, nil)
	l.Source = FakeSource{Events: events}
	l.Window = 40 * time.Millisecond
	l.SetInventoryForTest(map[string]string{"172.18.0.2": "bridge-ctr"}, nil, []string{"host-ctr"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	st := l.Status()
	if len(st.NotMonitoredContainerIds) != 1 || st.NotMonitoredContainerIds[0] != "host-ctr" {
		t.Fatalf("not_monitored=%v", st.NotMonitoredContainerIds)
	}
	// Even if somehow attributed, host-net ids are skipped in onEvent.
	events <- Event{Proto: "tcp", OrigSrc: netip.MustParseAddr("172.18.0.2"), OrigDst: netip.MustParseAddr("1.1.1.1"), OrigDstPort: 443}
	waitBatches(t, emit, 1, time.Second)
	cancel()
}

func TestUnavailableAndDegraded(t *testing.T) {
	events := make(chan Event)
	errs := make(chan error, 4)
	emit := &memEmit{}
	l := NewListener(nil, emit, nil)
	l.Source = FakeSource{Events: events, Errs: errs}
	l.Window = time.Hour
	l.MinBackoff = 20 * time.Millisecond
	l.MaxBackoff = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	errs <- unavailableError{"no nf_conntrack"}
	waitState(t, l, "unavailable", 3*time.Second)

	// After backoff the source is restarted; send degraded.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case errs <- degradedError{"netlink overflow"}:
		default:
		}
		if l.Status().GetState() == "degraded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitState(t, l, "degraded", 2*time.Second)
	cancel()
}

func TestRestartBackoffNoGoroutineLeak(t *testing.T) {
	events := make(chan Event)
	errs := make(chan error, 16)
	emit := &memEmit{}
	l := NewListener(nil, emit, nil)
	l.Source = FakeSource{Events: events, Errs: errs}
	l.Window = time.Hour
	l.MinBackoff = 5 * time.Millisecond
	l.MaxBackoff = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	before := runtime.NumGoroutine()
	go l.Run(ctx)
	for i := 0; i < 5; i++ {
		errs <- degradedError{"boom"}
		time.Sleep(40 * time.Millisecond)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+10 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

func TestKernelSourceSkipsWithoutNfConntrack(t *testing.T) {
	ok, reason := Available()
	if ok {
		t.Skip("nf_conntrack present; skip auto-skip assertion")
	}
	if reason == "" {
		t.Fatal("expected clear reason when unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := KernelSource{}.Run(ctx, func(Event) {})
	if err == nil {
		t.Fatal("expected error")
	}
	if !isUnavailable(err) {
		t.Fatalf("want unavailable, got %v", err)
	}
	t.Logf("kernel conntrack unavailable (expected on this VM): %v", err)
}

func BenchmarkHighRateHeap(b *testing.B) {
	events := make(chan Event, 10000)
	emit := &memEmit{}
	l := NewListener(nil, emit, nil)
	l.Source = FakeSource{Events: events}
	l.Window = 200 * time.Millisecond
	l.SetInventoryForTest(map[string]string{"172.18.0.2": "ctr"}, []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	startHeap := ms.HeapInuse

	src := netip.MustParseAddr("172.18.0.2")
	dst := netip.MustParseAddr("1.2.3.4")
	const rate = 20000
	const secs = 10
	deadline := time.Now().Add(secs * time.Second)
	sent := 0
	for time.Now().Before(deadline) {
		batchStart := time.Now()
		for i := 0; i < rate/100; i++ {
			select {
			case events <- Event{Proto: "tcp", OrigSrc: src, OrigDst: dst, OrigDstPort: uint16(i%1000 + 1)}:
				sent++
			default:
			}
		}
		elapsed := time.Since(batchStart)
		if sleep := 10*time.Millisecond - elapsed; sleep > 0 {
			time.Sleep(sleep)
		}
	}
	cancel()
	runtime.GC()
	runtime.ReadMemStats(&ms)
	growth := int64(ms.HeapInuse) - int64(startHeap)
	b.ReportMetric(float64(growth), "heap_growth_bytes")
	b.ReportMetric(float64(sent)/secs, "events_per_sec")
	b.Logf("fed ~%d events over %ds; heap in use growth = %d bytes (%.2f MB); target << 10 MB",
		sent, secs, growth, float64(growth)/(1<<20))
	if growth > 10<<20 {
		b.Fatalf("heap growth %d exceeds 10 MB", growth)
	}
}

func waitBatches(t *testing.T, e *memEmit, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(e.all()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d batches, have %d", n, len(e.all()))
}

func waitState(t *testing.T, l *Listener, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if l.Status().GetState() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state=%q want %q", l.Status().GetState(), want)
}

func itoa(u uint32) string {
	if u == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
