package briefing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// CatchUpWindow is how late a missed briefing may still be sent.
const CatchUpWindow = 6 * time.Hour

// TickInterval is how often the scheduler rechecks org briefing times.
const TickInterval = 30 * time.Second

// Enqueuer delivers an outgoing chat message (fan-out is the chat package's job).
// Defined here so briefing does not import internal/chat.
type Enqueuer interface {
	EnqueueMessage(ctx context.Context, orgID uuid.UUID, m store.ChatMessage) error
}

// Clock is an injectable time source for tests.
type Clock interface {
	Now() time.Time
}

// Store is the persistence the scheduler needs.
type Store interface {
	BriefingOrgs(ctx context.Context) ([]store.BriefingOrg, error)
	BriefingFacts(ctx context.Context, orgID uuid.UUID, since time.Time) (store.BriefingFacts, error)
	BriefingTryClaim(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error)
	BriefingReclaimUnenqueued(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error)
	BriefingMarkEnqueued(ctx context.Context, orgID uuid.UUID, localDate time.Time) error
	BriefingDelete(ctx context.Context, orgID uuid.UUID, localDate time.Time) error
}

// Scheduler sends each organisation's daily briefing once per local day.
type Scheduler struct {
	Store Store
	Enq   Enqueuer
	Clock Clock
	Log   *slog.Logger
	Every time.Duration // tick interval; zero means TickInterval
}

// Run ticks until ctx ends. Errors for one org do not stop the others.
func (s *Scheduler) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = TickInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	s.Check(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Check(ctx)
		}
	}
}

// Check evaluates every org once against the current clock.
func (s *Scheduler) Check(ctx context.Context) {
	if s.Enq == nil || s.Store == nil {
		return
	}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	now := s.now()
	orgs, err := s.Store.BriefingOrgs(ctx)
	if err != nil {
		log.Error("briefing list orgs", "err", err)
		return
	}
	for _, o := range orgs {
		if err := s.sendOrg(ctx, o, now); err != nil {
			log.Error("briefing org", "org", o.OrgID, "err", err)
		}
	}
}

func (s *Scheduler) now() time.Time {
	if s.Clock != nil {
		return s.Clock.Now()
	}
	return time.Now()
}

func (s *Scheduler) sendOrg(ctx context.Context, o store.BriefingOrg, now time.Time) error {
	loc, err := time.LoadLocation(o.TimeZone)
	if err != nil || o.TimeZone == "" {
		loc = time.UTC
	}
	hour := o.BriefingHour
	if hour < 0 || hour > 23 {
		hour = 8
	}

	local := now.In(loc)
	y, m, d := local.Date()
	// time.Date normalises a non-existent local time (DST spring-forward) to
	// the next valid minute; a repeated hour still shares the same local date.
	target := time.Date(y, m, d, hour, 0, 0, 0, loc)
	// HAETAE_BRIEFING_NOW is test-only and ignored unless HAETAE_TEST_HOOKS=1.
	force := os.Getenv("HAETAE_TEST_HOOKS") == "1" && os.Getenv("HAETAE_BRIEFING_NOW") != ""
	if !force {
		if now.Before(target) {
			return nil
		}
		if now.Sub(target) > CatchUpWindow {
			return nil
		}
	}

	localDate := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	claimed, err := s.Store.BriefingTryClaim(ctx, o.OrgID, localDate)
	if err != nil {
		return err
	}
	if !claimed {
		reclaimed, err := s.Store.BriefingReclaimUnenqueued(ctx, o.OrgID, localDate)
		if err != nil {
			return err
		}
		if !reclaimed {
			return nil
		}
	}

	since := now.Add(-24 * time.Hour)
	facts, err := s.Store.BriefingFacts(ctx, o.OrgID, since)
	if err != nil {
		_ = s.Store.BriefingDelete(ctx, o.OrgID, localDate)
		return err
	}
	body := Compose(facts, now, loc)
	day := localDate.UTC().Format("2006-01-02")
	msg := store.ChatMessage{
		Channel:   store.ChannelWeb,
		Direction: "out",
		Kind:      store.KindBriefing,
		Body:      body,
		Actions:   nil,
		Evidence: []store.ChatEvidence{
			{Label: "hosts", Path: "/hosts"},
			{Label: "all incidents", Path: "/incidents"},
		},
		// Deterministic per (org, local_date); fan-out appends channel/account.
		IdempotencyKey: fmt.Sprintf("briefing:%s:%s", o.OrgID, day),
	}
	if err := s.Enq.EnqueueMessage(ctx, o.OrgID, msg); err != nil {
		_ = s.Store.BriefingDelete(ctx, o.OrgID, localDate)
		return err
	}
	if err := s.Store.BriefingMarkEnqueued(ctx, o.OrgID, localDate); err != nil {
		return err
	}
	return nil
}
