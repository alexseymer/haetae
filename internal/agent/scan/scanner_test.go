package scan

import (
	"context"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

type fakeRunner struct {
	mu   sync.Mutex
	reqs []LaunchRequest
	fn   func(LaunchRequest) LaunchResult
}

func (f *fakeRunner) Run(_ context.Context, req LaunchRequest) LaunchResult {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return LaunchResult{
		Status: StatusOK,
		Doc: &HelperDocument{
			ToolVersion: "t",
			ImageID:     req.DockerImage,
			Packages:    []HelperPkg{{Name: "musl", Version: "1", Type: "apk"}},
		},
	}
}

type fakeOut struct {
	mu  sync.Mutex
	got []*agentv1.SbomReport
}

func (o *fakeOut) PutSbom(r *agentv1.SbomReport) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, r)
}

func TestScannerDedupeAndOrder(t *testing.T) {
	out := &fakeOut{}
	run := &fakeRunner{}
	s := NewScanner(Config{QueueCap: 8, DailyEvery: time.Hour}, nil, run, out, nil)
	s.SetRunningForTest([]containerInfo{
		{ID: "c1", Name: "one", Image: "alpine:3.20", ImageID: "sha256:aaa"},
		{ID: "c2", Name: "two", Image: "alpine:3.20", ImageID: "sha256:aaa"},
		{ID: "c3", Name: "three", Image: "busybox", ImageID: "sha256:bbb"},
	})
	s.OnDeployHealthy("proj")
	if s.QueueLen() != 2 {
		t.Fatalf("queue=%d want 2 (deduped aaa)", s.QueueLen())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitReports(t, out, 2, 2*time.Second)
	cancel()
	<-done

	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.reqs) != 2 {
		t.Fatalf("ran %d jobs", len(run.reqs))
	}
	// FIFO: aaa then bbb (map iteration order for OnDeployHealthy is nondeterministic!).
	// Check set membership instead.
	seen := map[string]bool{}
	for _, r := range run.reqs {
		seen[r.DockerImage] = true
	}
	if !seen["sha256:aaa"] || !seen["sha256:bbb"] {
		t.Fatalf("reqs=%v", run.reqs)
	}
}

func TestScannerQueueBounded(t *testing.T) {
	out := &fakeOut{}
	run := &fakeRunner{fn: func(LaunchRequest) LaunchResult {
		time.Sleep(50 * time.Millisecond)
		return LaunchResult{Status: StatusOK, Doc: &HelperDocument{ToolVersion: "t"}}
	}}
	s := NewScanner(Config{QueueCap: 2}, nil, run, out, nil)
	for i := 0; i < 5; i++ {
		id := "img-" + string(rune('a'+i))
		s.EnqueueForTest(job{scanID: id, imageID: id, image: id, trigger: TriggerDaily})
	}
	if s.QueueLen() > 2 {
		t.Fatalf("queue=%d", s.QueueLen())
	}
}

func TestScannerDailyJitter(t *testing.T) {
	var nowMu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	jitterCalls := 0
	s := NewScanner(Config{
		DailyEvery: time.Hour,
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		},
		RandJitter: func(max time.Duration) time.Duration {
			jitterCalls++
			return 5 * time.Minute
		},
	}, nil, &fakeRunner{}, &fakeOut{}, nil)
	s.SetRunningForTest([]containerInfo{{ID: "c", Image: "a", ImageID: "id1"}})
	delay := s.nextDailyDelay()
	if delay != time.Hour+5*time.Minute {
		t.Fatalf("delay=%v", delay)
	}
	if jitterCalls == 0 {
		t.Fatal("jitter not called")
	}
	s.EnqueueDailyForTest()
	if s.QueueLen() != 1 {
		t.Fatalf("queue=%d", s.QueueLen())
	}
}

func TestScannerCommands(t *testing.T) {
	out := &fakeOut{}
	run := &fakeRunner{}
	s := NewScanner(Config{}, nil, run, out, nil)
	// ScanImage does not need docker.
	res := s.HandleCommand(context.Background(), &agentv1.Command{
		Id: "cmd1",
		Kind: &agentv1.Command_ScanImage{ScanImage: &agentv1.ScanImage{
			ScanId: "s1", Ref: "alpine", Digest: "sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		}},
	})
	if res == nil || res.Error != "" {
		t.Fatalf("res=%v", res)
	}
	if s.QueueLen() != 1 {
		t.Fatalf("queue=%d", s.QueueLen())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitReports(t, out, 1, 2*time.Second)
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.got[0].GetTrigger() != TriggerCandidate || out.got[0].GetScanId() != "s1" {
		t.Fatalf("report=%v", out.got[0])
	}
}

func waitReports(t *testing.T, out *fakeOut, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out.mu.Lock()
		got := len(out.got)
		out.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	t.Fatalf("got %d reports, want %d", len(out.got), n)
}
