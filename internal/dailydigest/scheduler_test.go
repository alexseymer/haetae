package dailydigest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type run struct{ done, posted bool }

// memStore is the digest persistence in memory: runs keyed by (org, day).
type memStore struct {
	mu         sync.Mutex
	orgs       []store.DigestOrg
	runs       map[string]*run
	watermark  time.Time
	watermarks int
}

func key(org uuid.UUID, day time.Time) string {
	return org.String() + "|" + day.UTC().Format("2006-01-02")
}

func (m *memStore) DigestOrgs(context.Context) ([]store.DigestOrg, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.DigestOrg(nil), m.orgs...), nil
}

func (m *memStore) DigestTryClaim(_ context.Context, org uuid.UUID, day time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[key(org, day)]; ok {
		return false, nil
	}
	m.runs[key(org, day)] = &run{}
	return true, nil
}

func (m *memStore) DigestReclaim(context.Context, uuid.UUID, time.Time) (bool, error) {
	return false, nil
}

func (m *memStore) DigestFinish(_ context.Context, org uuid.UUID, day time.Time, posted bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[key(org, day)]
	r.done, r.posted = true, posted
	return nil
}

func (m *memStore) DigestDelete(_ context.Context, org uuid.UUID, day time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.runs[key(org, day)]; r != nil && !r.done {
		delete(m.runs, key(org, day))
	}
	return nil
}

func (m *memStore) SetSecurityDigestWatermark(_ context.Context, _ uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at.After(m.watermark) {
		m.watermark = at
	}
	m.watermarks++
	return nil
}

// memEnq records the messages the scheduler enqueues.
type memEnq struct {
	mu   sync.Mutex
	msgs []store.ChatMessage
}

func (e *memEnq) EnqueueMessage(_ context.Context, _ uuid.UUID, m store.ChatMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.msgs = append(e.msgs, m)
	return nil
}

// fakeDigester returns canned digests; calls counts the AI-facing calls.
type fakeDigester struct {
	mu            sync.Mutex
	logAttention  bool
	secAttention  bool
	calls         int
	logErr        error
	secWindowTo   time.Time
	logStatements []string
}

func (f *fakeDigester) SummarizeLogs(context.Context, uuid.UUID, operator.LogScope) (operator.Digest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.logErr != nil {
		return operator.Digest{}, f.logErr
	}
	return digestOf(operator.DigestLogs, "Log summary: all hosts, last 24h", f.logAttention, f.logStatements...), nil
}

func (f *fakeDigester) SecurityDigest(context.Context, uuid.UUID, operator.SecurityScope) (operator.Digest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	d := digestOf(operator.DigestSecurity, "Security digest", f.secAttention, "Two critical findings on hel1.")
	d.WindowTo = f.secWindowTo
	return d, nil
}

// digestOf builds a rule-style digest. Quiet is the opposite of attention.
func digestOf(kind, title string, attention bool, statements ...string) operator.Digest {
	d := operator.Digest{Kind: kind, Title: title, NeedsAttention: attention, Quiet: !attention}
	for _, s := range statements {
		d.Statements = append(d.Statements, operator.Statement{Text: s, Evidence: []string{}})
	}
	if attention {
		d.Evidence = []operator.Evidence{{ID: "ev_1", Title: "Errors on hel1", Link: "/hosts/x/logs"}}
	}
	return d
}

var helsinki, _ = time.LoadLocation("Europe/Helsinki")

// at returns a UTC instant for a Helsinki wall-clock time on 2026-10-29.
func at(h, m int) time.Time {
	return time.Date(2026, 10, 29, h, m, 0, 0, helsinki).UTC()
}

func newScheduler(st *memStore, enq *memEnq, dg *fakeDigester, clk *fixedClock) *Scheduler {
	return &Scheduler{Store: st, Enq: enq, Digests: dg, Clock: clk, Log: quietLog()}
}

func TestDailyDigestPostsOnceOnAttentionDay(t *testing.T) {
	org := uuid.New()
	st := &memStore{orgs: []store.DigestOrg{{OrgID: org, TimeZone: "Europe/Helsinki", BriefingHour: 8, Enabled: true}}, runs: map[string]*run{}}
	enq := &memEnq{}
	dg := &fakeDigester{logAttention: true, secAttention: true, logStatements: []string{"Error pattern new on hel1."}, secWindowTo: at(8, 5)}
	clk := &fixedClock{}
	s := newScheduler(st, enq, dg, clk)

	// Before the briefing hour plus the delay: nothing, and no AI call.
	clk.Set(at(8, 4))
	s.Check(context.Background())
	if len(enq.msgs) != 0 || dg.calls != 0 {
		t.Fatalf("before the delay: %d messages, %d digest calls", len(enq.msgs), dg.calls)
	}

	// Due: one message of kind digest with both digests.
	clk.Set(at(8, 6))
	s.Check(context.Background())
	if len(enq.msgs) != 1 {
		t.Fatalf("messages after due: %d", len(enq.msgs))
	}
	m := enq.msgs[0]
	if m.Kind != store.KindDigest || m.Channel != store.ChannelWeb || m.Direction != "out" {
		t.Fatalf("message: kind %q channel %q direction %q", m.Kind, m.Channel, m.Direction)
	}
	if !strings.Contains(m.Body, "Log summary") || !strings.Contains(m.Body, "Security digest") || !strings.Contains(m.Body, "Error pattern new on hel1.") {
		t.Fatalf("body lacks a digest: %q", m.Body)
	}
	if m.IdempotencyKey == "" || len(m.Evidence) == 0 || m.Evidence[0].Path != "/hosts/x/logs" {
		t.Fatalf("key %q evidence %+v", m.IdempotencyKey, m.Evidence)
	}

	// Later ticks the same day, and a second pass: still one message.
	clk.Set(at(9, 0))
	s.Check(context.Background())
	clk.Set(at(20, 0))
	s.Check(context.Background())
	if len(enq.msgs) != 1 {
		t.Fatalf("messages after more ticks the same day: %d", len(enq.msgs))
	}
	if !st.runs[key(org, at(8, 6))].posted {
		t.Fatal("run not finished as posted")
	}
	if !st.watermark.Equal(at(8, 5)) {
		t.Fatalf("watermark %v, want %v", st.watermark, at(8, 5))
	}
}

func TestDailyDigestStaysSilentOnQuietDay(t *testing.T) {
	org := uuid.New()
	st := &memStore{orgs: []store.DigestOrg{{OrgID: org, TimeZone: "Europe/Helsinki", BriefingHour: 8, Enabled: true}}, runs: map[string]*run{}}
	enq := &memEnq{}
	dg := &fakeDigester{secWindowTo: at(8, 5)}
	clk := &fixedClock{now: at(8, 30)}
	s := newScheduler(st, enq, dg, clk)

	s.Check(context.Background())
	s.Check(context.Background())
	if len(enq.msgs) != 0 {
		t.Fatalf("quiet day posted %d messages", len(enq.msgs))
	}
	if dg.calls != 2 {
		t.Fatalf("digest calls %d, want 2 (one run, both digests)", dg.calls)
	}
	r := st.runs[key(org, at(8, 30))]
	if r == nil || !r.done || r.posted {
		t.Fatalf("run %+v: want done and not posted", r)
	}
	// A silent run posted nothing, so the watermark must not move: its findings
	// stay new for the next day that does post.
	if st.watermarks != 0 || !st.watermark.IsZero() {
		t.Fatalf("watermark moves %d, value %v; want none on a silent run", st.watermarks, st.watermark)
	}
}

func TestDailyDigestPendingOnlyDayPostsOneMessage(t *testing.T) {
	org := uuid.New()
	st := &memStore{orgs: []store.DigestOrg{{OrgID: org, TimeZone: "Europe/Helsinki", BriefingHour: 8, Enabled: true}}, runs: map[string]*run{}}
	enq := &memEnq{}
	// Only the security digest needs attention: a pending scan, no log finding.
	dg := &fakeDigester{secAttention: true, secWindowTo: at(8, 5)}
	clk := &fixedClock{now: at(8, 30)}
	s := newScheduler(st, enq, dg, clk)

	s.Check(context.Background())
	s.Check(context.Background())
	if len(enq.msgs) != 1 {
		t.Fatalf("pending-only day posted %d messages, want 1", len(enq.msgs))
	}
	if strings.Contains(enq.msgs[0].Body, "Log summary") || !strings.Contains(enq.msgs[0].Body, "Security digest") {
		t.Fatalf("body %q: want the security digest only", enq.msgs[0].Body)
	}
	if st.watermarks != 1 || !st.watermark.Equal(at(8, 5)) {
		t.Fatalf("watermark moves %d, value %v; want one move to %v", st.watermarks, st.watermark, at(8, 5))
	}
}

func TestDailyDigestSkipsDisabledOrgsAndRetriesAfterError(t *testing.T) {
	off, on := uuid.New(), uuid.New()
	st := &memStore{orgs: []store.DigestOrg{
		{OrgID: off, TimeZone: "UTC", BriefingHour: 8, Enabled: false},
		{OrgID: on, TimeZone: "UTC", BriefingHour: 8, Enabled: true},
	}, runs: map[string]*run{}}
	enq := &memEnq{}
	dg := &fakeDigester{logAttention: true, logErr: errors.New("store down")}
	clk := &fixedClock{now: time.Date(2026, 10, 29, 8, 10, 0, 0, time.UTC)}
	s := newScheduler(st, enq, dg, clk)

	// The disabled org never reaches the digester; the failing one is retried.
	s.Check(context.Background())
	if len(enq.msgs) != 0 || dg.calls != 1 {
		t.Fatalf("first pass: %d messages, %d calls", len(enq.msgs), dg.calls)
	}
	if _, ok := st.runs[key(on, time.Date(2026, 10, 29, 8, 10, 0, 0, time.UTC))]; ok {
		t.Fatal("failed run kept its claim")
	}
	dg.logErr = nil
	s.Check(context.Background())
	if len(enq.msgs) != 1 || enq.msgs[0].IdempotencyKey == "" {
		t.Fatalf("retry: %d messages", len(enq.msgs))
	}
	if _, ok := st.runs[key(off, time.Date(2026, 10, 29, 8, 10, 0, 0, time.UTC))]; ok {
		t.Fatal("disabled org has a run")
	}
}
