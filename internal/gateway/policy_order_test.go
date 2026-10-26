package gateway

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type autonomyStore struct {
	Store
	mu    sync.Mutex
	level string
	first chan struct{} // first read returns its value, then waits for this
	read  chan struct{}
	n     atomic.Int32
}

func (a *autonomyStore) OrgAutonomy(context.Context, uuid.UUID) (string, error) {
	a.mu.Lock()
	v := a.level
	a.mu.Unlock()
	if a.n.Add(1) == 1 {
		close(a.read)
		<-a.first
	}
	return v, nil
}

// A slow push that read an older level must not overwrite a newer one.
func TestPushPolicyIsSerialisedPerOrg(t *testing.T) {
	org := uuid.New()
	st := &autonomyStore{level: "act", first: make(chan struct{}), read: make(chan struct{})}
	g := New(st, nil, Config{}, slog.New(slog.DiscardHandler))
	var sessions []*session
	for range 3 {
		s := newSession(uuid.New(), func(error) {})
		s.orgID = org
		g.sessions[s.hostID] = s
		sessions = append(sessions, s)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); g.PushPolicy(org) }()
	<-st.read // first push has read "act" and is stalled
	st.mu.Lock()
	st.level = "report"
	st.mu.Unlock()
	go func() { defer wg.Done(); g.PushPolicy(org) }()
	time.Sleep(50 * time.Millisecond)
	close(st.first)
	wg.Wait()
	for _, s := range sessions {
		select {
		case got := <-s.policy:
			if got != "report" {
				t.Fatalf("session policy = %q, want report (stale push won)", got)
			}
		default:
			t.Fatal("no policy pushed")
		}
	}
}

func TestPushPolicyConcurrentConverges(t *testing.T) {
	org := uuid.New()
	st := &autonomyStore{level: "report", first: make(chan struct{}), read: make(chan struct{})}
	close(st.first)
	g := New(st, nil, Config{}, slog.New(slog.DiscardHandler))
	var sessions []*session
	for range 4 {
		s := newSession(uuid.New(), func(error) {})
		s.orgID = org
		g.sessions[s.hostID] = s
		sessions = append(sessions, s)
	}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.mu.Lock()
			st.level = []string{"report", "suggest", "act"}[i%3]
			st.mu.Unlock()
			g.PushPolicy(org)
		}()
	}
	wg.Wait()
	g.PushPolicy(org)
	st.mu.Lock()
	want := st.level
	st.mu.Unlock()
	for _, s := range sessions {
		if got := <-s.policy; got != want {
			t.Fatalf("level %q, want %q", got, want)
		}
	}
}
