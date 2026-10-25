//go:build integration

package briefing_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/briefing"
	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestBriefingEnqueueWithoutMarkEnqueuedNoDuplicate(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "brief-idem")
	if err != nil {
		t.Fatal(err)
	}
	svc := chat.New(chat.Config{
		Store: s, Secret: []byte("0123456789abcdef0123456789abcdef"),
		Channels: []chat.Channel{web.Channel{}},
	})
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	ok, err := s.BriefingTryClaim(ctx, org.ID, day)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	msg := store.ChatMessage{
		Channel:        store.ChannelWeb,
		Kind:           store.KindBriefing,
		Body:           "08:00 briefing, 0 hosts, 0 containers, all running.",
		IdempotencyKey: "briefing:" + org.ID.String() + ":2026-10-25",
	}
	if err := svc.EnqueueMessage(ctx, org.ID, msg); err != nil {
		t.Fatal(err)
	}
	// Crash: MarkEnqueued never runs. Expire lease and reclaim.
	_, _ = s.Pool().Exec(ctx, `
		UPDATE briefings SET lease_until = now() - interval '1 second'
		WHERE org_id = $1 AND local_date = $2::date`, org.ID, "2026-10-25")

	clock := fixedClock{t: time.Date(2026, 10, 25, 8, 5, 0, 0, time.UTC)}
	sched := &briefing.Scheduler{
		Store: s, Enq: svc, Clock: clock,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	sched.Check(ctx)
	sched.Check(ctx)

	var n int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM chat_messages
		WHERE org_id = $1 AND kind = 'briefing'`, org.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("briefing messages=%d want 1", n)
	}
}

func TestBriefingConcurrentReclaimOneWinner(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "brief-race")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	ok, err := s.BriefingTryClaim(ctx, org.ID, day)
	if err != nil || !ok {
		t.Fatal(err)
	}
	_, _ = s.Pool().Exec(ctx, `
		UPDATE briefings SET lease_until = now() - interval '1 second'
		WHERE org_id = $1 AND local_date = $2::date`, org.ID, "2026-10-25")

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.BriefingReclaimUnenqueued(ctx, org.ID, day)
			if err != nil {
				t.Errorf("%v", err)
				return
			}
			if got {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("winners=%d want 1", wins.Load())
	}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }
