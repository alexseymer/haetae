package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func TestWatcherReportsChangedCommitOnce(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(FakeStep{
		Match:  matchCmd("git", "ls-remote"),
		Stdout: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/heads/main"},
		Repeat: -1,
	})
	w := &Watcher{Runner: runner, Interval: time.Hour}
	w.SetWatchSet(&agentv1.WatchSet{Branches: []*agentv1.WatchedBranch{{
		Project:    "shop",
		Url:        "https://github.com/acme/shop",
		Ref:        "main",
		LastCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}})

	var reports []*agentv1.BranchUpdate
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, func(u *agentv1.BranchUpdate) {
			mu.Lock()
			reports = append(reports, u)
			mu.Unlock()
			cancel()
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || reports[0].GetCommit() != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("reports = %+v", reports)
	}
	// Second poll with same commit should not report again.
	w.SetWatchSet(&agentv1.WatchSet{Branches: []*agentv1.WatchedBranch{{
		Project:    "shop",
		Url:        "https://github.com/acme/shop",
		Ref:        "main",
		LastCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	w.Run(ctx2, func(u *agentv1.BranchUpdate) {
		t.Errorf("unexpected report %+v", u)
	})
}

func TestWatcherEmptyLastCommitReportsOnce(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(FakeStep{
		Match:  matchCmd("git", "ls-remote"),
		Stdout: []string{"cccccccccccccccccccccccccccccccccccccccc\trefs/heads/main"},
		Repeat: -1,
	})
	w := &Watcher{Runner: runner, Interval: time.Hour}
	w.SetWatchSet(&agentv1.WatchSet{Branches: []*agentv1.WatchedBranch{{
		Project: "shop",
		Url:     "https://github.com/acme/shop",
		Ref:     "main",
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	go w.Run(ctx, func(*agentv1.BranchUpdate) {
		if n.Add(1) == 1 {
			cancel()
		}
	})
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout")
	}
	if n.Load() != 1 {
		t.Fatalf("reports = %d", n.Load())
	}
}

func TestWatcherHandlesErrors(t *testing.T) {
	runner := &FakeRunner{}
	runner.Add(FakeStep{Match: matchCmd("git", "ls-remote"), Err: errors.New("network"), Repeat: -1})
	w := &Watcher{Runner: runner, Interval: time.Hour}
	w.SetWatchSet(&agentv1.WatchSet{Branches: []*agentv1.WatchedBranch{{
		Project: "shop", Url: "https://github.com/acme/shop", Ref: "main",
	}}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	w.Run(ctx, func(u *agentv1.BranchUpdate) {
		t.Errorf("unexpected %+v", u)
	})
}

func TestWatcherCapsConcurrency(t *testing.T) {
	var concurrent atomic.Int32
	var max atomic.Int32
	runner := &concurrencyFake{active: &concurrent, max: &max}
	branches := make([]*agentv1.WatchedBranch, 20)
	for i := range branches {
		branches[i] = &agentv1.WatchedBranch{
			Project: fmt.Sprintf("proj-%d", i),
			Url:     "https://github.com/acme/shop",
			Ref:     "main",
		}
	}
	w := &Watcher{Runner: runner, Interval: time.Hour}
	w.SetWatchSet(&agentv1.WatchSet{Branches: branches})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.Run(ctx, nil)
	if max.Load() > 8 {
		t.Fatalf("max concurrent = %d", max.Load())
	}
	if max.Load() < 2 {
		t.Fatalf("expected some concurrency, max=%d", max.Load())
	}
}

type concurrencyFake struct {
	active, max *atomic.Int32
}

func (c *concurrencyFake) Run(ctx context.Context, cmd Cmd) error {
	n := c.active.Add(1)
	for {
		old := c.max.Load()
		if n <= old || c.max.CompareAndSwap(old, n) {
			break
		}
	}
	defer c.active.Add(-1)
	time.Sleep(20 * time.Millisecond)
	if cmd.Stdout != nil {
		cmd.Stdout("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/heads/main")
	}
	return ctx.Err()
}

func TestWatcherSkipsCommitRefs(t *testing.T) {
	runner := &FakeRunner{}
	w := &Watcher{Runner: runner, Interval: time.Hour}
	w.SetWatchSet(&agentv1.WatchSet{Branches: []*agentv1.WatchedBranch{{
		Project: "shop",
		Url:     "https://github.com/acme/shop",
		Ref:     "0123456789abcdef0123456789abcdef01234567",
	}}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	w.Run(ctx, func(*agentv1.BranchUpdate) { t.Fatal("should not report") })
	if len(runner.Calls) != 0 {
		t.Fatalf("ls-remote called for commit ref: %v", runner.Calls)
	}
}
