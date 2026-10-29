// Package dailydigest posts each organisation's daily AI digest (the log
// summary of the last day and the security digest) as one chat message, and
// only on a day with something to say. It runs on its own goroutine and never
// shares the briefing's or the alert evaluator's loop.
package dailydigest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

const (
	// Delay is how long after the briefing hour the digest is due, so the
	// briefing is always the first message of the morning.
	Delay = 5 * time.Minute
	// CatchUpWindow is how late a missed digest may still be posted.
	CatchUpWindow = 6 * time.Hour
	// TickInterval is how often the scheduler rechecks the organisations.
	TickInterval = 30 * time.Second
	// OrgTimeout bounds one organisation's whole run, AI calls included. It is
	// shorter than the run lease (store.BriefingLeaseTTL), so a run never
	// outlives its lease.
	OrgTimeout = 4 * time.Minute
	// LogWindow is the log window of the daily digest.
	LogWindow = 24 * time.Hour
	// maxEvidence caps the links carried by the one message.
	maxEvidence = 12
)

// Enqueuer delivers an outgoing chat message (fan-out is the chat package's job).
type Enqueuer interface {
	EnqueueMessage(ctx context.Context, orgID uuid.UUID, m store.ChatMessage) error
}

// Digester produces the two digests. The operator implements it; without the
// AI it returns the rule-based text.
type Digester interface {
	SummarizeLogs(ctx context.Context, orgID uuid.UUID, scope operator.LogScope) (operator.Digest, error)
	SecurityDigest(ctx context.Context, orgID uuid.UUID, scope operator.SecurityScope) (operator.Digest, error)
}

// Clock is an injectable time source for tests.
type Clock interface {
	Now() time.Time
}

// Store is the persistence the scheduler needs.
type Store interface {
	DigestOrgs(ctx context.Context) ([]store.DigestOrg, error)
	DigestTryClaim(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error)
	DigestReclaim(ctx context.Context, orgID uuid.UUID, localDate time.Time) (bool, error)
	DigestFinish(ctx context.Context, orgID uuid.UUID, localDate time.Time, posted bool) error
	DigestDelete(ctx context.Context, orgID uuid.UUID, localDate time.Time) error
	SetSecurityDigestWatermark(ctx context.Context, orgID uuid.UUID, at time.Time) error
}

// Scheduler posts each organisation's digest once per local day, if it has
// something to say.
type Scheduler struct {
	Store   Store
	Enq     Enqueuer
	Digests Digester
	Clock   Clock
	Log     *slog.Logger
	Every   time.Duration // tick interval; zero means TickInterval
	Timeout time.Duration // per organisation; zero means OrgTimeout
}

// Run ticks until ctx ends. A failure or panic for one org does not stop the others.
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

// Check evaluates every organisation once against the current clock.
func (s *Scheduler) Check(ctx context.Context) {
	if s.Enq == nil || s.Store == nil || s.Digests == nil {
		return
	}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	now := s.now()
	orgs, err := s.Store.DigestOrgs(ctx)
	if err != nil {
		log.Error("digest list orgs", "err", err)
		return
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = OrgTimeout
	}
	for _, o := range orgs {
		if ctx.Err() != nil {
			return
		}
		s.runOrg(ctx, log, timeout, o, now)
	}
}

func (s *Scheduler) runOrg(ctx context.Context, log *slog.Logger, timeout time.Duration, o store.DigestOrg, now time.Time) {
	octx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			log.Error("digest org panic", "org", o.OrgID, "panic", r)
		}
	}()
	if err := s.sendOrg(octx, o, now); err != nil {
		log.Error("digest org", "org", o.OrgID, "err", err)
	}
}

func (s *Scheduler) now() time.Time {
	if s.Clock != nil {
		return s.Clock.Now()
	}
	return time.Now()
}

func (s *Scheduler) sendOrg(ctx context.Context, o store.DigestOrg, now time.Time) error {
	if !o.Enabled {
		return nil
	}
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
	// Same local date rule as the briefing: a DST gap normalises the target.
	target := time.Date(y, m, d, hour, 0, 0, 0, loc).Add(Delay)
	if now.Before(target) || now.Sub(target) > CatchUpWindow {
		return nil
	}

	localDate := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	claimed, err := s.Store.DigestTryClaim(ctx, o.OrgID, localDate)
	if err != nil {
		return err
	}
	if !claimed {
		reclaimed, err := s.Store.DigestReclaim(ctx, o.OrgID, localDate)
		if err != nil {
			return err
		}
		if !reclaimed {
			return nil
		}
	}

	posted, err := s.run(ctx, o, localDate)
	if err != nil {
		// The message is keyed per (org, day), so a retry cannot post twice.
		_ = s.Store.DigestDelete(ctx, o.OrgID, localDate)
		return err
	}
	return s.Store.DigestFinish(ctx, o.OrgID, localDate, posted)
}

// run builds both digests, posts one message when either needs attention and
// moves the security watermark. It reports whether a message was enqueued.
func (s *Scheduler) run(ctx context.Context, o store.DigestOrg, localDate time.Time) (bool, error) {
	logD, err := s.Digests.SummarizeLogs(ctx, o.OrgID, operator.LogScope{Window: LogWindow})
	if err != nil {
		return false, err
	}
	secD, err := s.Digests.SecurityDigest(ctx, o.OrgID, operator.SecurityScope{})
	if err != nil {
		return false, err
	}

	var parts []string
	var ev []store.ChatEvidence
	for _, d := range []operator.Digest{logD, secD} {
		if d.Quiet || !d.NeedsAttention {
			continue
		}
		parts = append(parts, d.Text())
		for _, e := range d.Evidence {
			if strings.HasPrefix(e.Link, "/") && !strings.HasPrefix(e.Link, "//") && len(ev) < maxEvidence {
				ev = append(ev, store.ChatEvidence{Label: e.Title, Path: e.Link})
			}
		}
	}

	posted := false
	if len(parts) > 0 {
		msg := store.ChatMessage{
			Channel:        store.ChannelWeb,
			Direction:      "out",
			Kind:           store.KindDigest,
			Body:           strings.Join(parts, "\n\n"),
			Evidence:       ev,
			IdempotencyKey: fmt.Sprintf("digest:%s:%s", o.OrgID, localDate.Format("2006-01-02")),
		}
		if err := s.Enq.EnqueueMessage(ctx, o.OrgID, msg); err != nil {
			return false, err
		}
		posted = true
	}
	// Findings first seen after the watermark are new in the next digest. The
	// watermark moves on quiet days too, so nothing is reported twice.
	if err := s.Store.SetSecurityDigestWatermark(ctx, o.OrgID, secD.WindowTo); err != nil {
		return posted, err
	}
	return posted, nil
}
