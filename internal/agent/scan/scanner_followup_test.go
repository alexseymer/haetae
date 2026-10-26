package scan

import (
	"context"
	"testing"
	"time"
)

func TestForcedWhileRunningKeepsFollowUp(t *testing.T) {
	out := &fakeOut{}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	run := &fakeRunner{fn: func(LaunchRequest) LaunchResult {
		started <- struct{}{}
		<-release
		return LaunchResult{Status: StatusOK, Doc: &HelperDocument{ToolVersion: "t"}}
	}}
	s := NewScanner(Config{QueueCap: 8}, nil, run, out, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	s.mu.Lock()
	s.enqueueLocked(job{scanID: "first", imageID: "img", image: "img", trigger: TriggerStart})
	s.mu.Unlock()
	<-started // first is running
	s.mu.Lock()
	s.enqueueForcedLocked(job{scanID: "forced", imageID: "img", image: "img", trigger: TriggerForced})
	s.mu.Unlock()
	// A non-forced enqueue for the same image must still be deduped against the follow-up.
	s.EnqueueForTest(job{scanID: "dup", imageID: "img", image: "img", trigger: TriggerDaily})
	if s.QueueLen() != 1 {
		t.Fatalf("queue=%d want 1 follow-up", s.QueueLen())
	}
	release <- struct{}{} // finish first; its deferred cleanup must not clear the follow-up mark
	<-started
	release <- struct{}{}
	waitReports(t, out, 2, 2*time.Second)
	ids := []string{out.got[0].ScanId, out.got[1].ScanId}
	if ids[0] != "first" || ids[1] != "forced" {
		t.Fatalf("reports=%v", ids)
	}
}

func TestQueueFullReportsFailedForDropped(t *testing.T) {
	out := &fakeOut{}
	s := NewScanner(Config{QueueCap: 2}, nil, &fakeRunner{}, out, nil)
	for _, id := range []string{"a", "b", "c"} {
		s.EnqueueForTest(job{scanID: "scan-" + id, imageID: id, image: id, trigger: TriggerForced})
	}
	if s.QueueLen() != 2 {
		t.Fatalf("queue=%d", s.QueueLen())
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(out.got) != 1 || out.got[0].ScanId != "scan-a" || out.got[0].Status != StatusFailed || out.got[0].Error != "scan queue full" {
		t.Fatalf("reports=%v", out.got)
	}
}

func TestOnRunningCoalescesLatestWins(t *testing.T) {
	s := NewScanner(Config{}, nil, &fakeRunner{}, &fakeOut{}, nil)
	s.OnRunning(map[string]string{"a": "1"})
	s.OnRunning(map[string]string{"b": "2"})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.latestRun) != 1 || s.latestRun["b"] != "2" {
		t.Fatalf("latest=%v", s.latestRun)
	}
}

func TestForcedReplacingQueuedReportsSuperseded(t *testing.T) {
	out := &fakeOut{}
	s := NewScanner(Config{QueueCap: 4}, nil, &fakeRunner{}, out, nil)
	s.mu.Lock()
	s.enqueueForcedLocked(job{scanID: "scan-1", imageID: "a", image: "a", trigger: TriggerForced})
	s.enqueueForcedLocked(job{scanID: "scan-2", imageID: "a", image: "a", trigger: TriggerForced})
	s.mu.Unlock()
	if s.QueueLen() != 1 {
		t.Fatalf("queue=%d", s.QueueLen())
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(out.got) != 1 || out.got[0].ScanId != "scan-1" || out.got[0].Status != StatusFailed {
		t.Fatalf("reports=%v", out.got)
	}
}
