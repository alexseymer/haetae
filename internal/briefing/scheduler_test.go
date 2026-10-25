package briefing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type memClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *memClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *memClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type memStore struct {
	mu      sync.Mutex
	orgs    []store.BriefingOrg
	facts   map[uuid.UUID]store.BriefingFacts
	claimed map[string]struct{}
}

func newMemStore(orgs ...store.BriefingOrg) *memStore {
	return &memStore{
		orgs:    orgs,
		facts:   map[uuid.UUID]store.BriefingFacts{},
		claimed: map[string]struct{}{},
	}
}

func claimKey(org uuid.UUID, day time.Time) string {
	return org.String() + "|" + day.UTC().Format("2006-01-02")
}

func (m *memStore) BriefingOrgs(context.Context) ([]store.BriefingOrg, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]store.BriefingOrg(nil), m.orgs...)
	return out, nil
}

func (m *memStore) BriefingFacts(_ context.Context, orgID uuid.UUID, _ time.Time) (store.BriefingFacts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.facts[orgID]; ok {
		return f, nil
	}
	return store.BriefingFacts{Hosts: 1, Online: 1, Containers: map[string]int{"running": 1}}, nil
}

func (m *memStore) BriefingTryClaim(_ context.Context, orgID uuid.UUID, localDate time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := claimKey(orgID, localDate)
	if _, ok := m.claimed[k]; ok {
		return false, nil
	}
	m.claimed[k] = struct{}{}
	return true, nil
}

func (m *memStore) BriefingDelete(_ context.Context, orgID uuid.UUID, localDate time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claimed, claimKey(orgID, localDate))
	return nil
}

func (m *memStore) claims() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.claimed)
}

type memEnq struct {
	mu   sync.Mutex
	msgs []enqCall
	fail int // fail the next N enqueue calls
	err  error
}

type enqCall struct {
	Org uuid.UUID
	Msg store.ChatMessage
}

func (e *memEnq) EnqueueMessage(_ context.Context, orgID uuid.UUID, m store.ChatMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail > 0 {
		e.fail--
		if e.err != nil {
			return e.err
		}
		return errors.New("enqueue failed")
	}
	e.msgs = append(e.msgs, enqCall{Org: orgID, Msg: m})
	return nil
}

func (e *memEnq) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.msgs)
}

func (e *memEnq) bodies() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.msgs))
	for i, c := range e.msgs {
		out[i] = c.Msg.Body
	}
	return out
}

func TestSchedulerOncePerDayRestartAndConcurrent(t *testing.T) {
	org := uuid.New()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	clock := &memClock{now: time.Date(2026, 6, 15, 8, 0, 30, 0, berlin)}
	st := newMemStore(store.BriefingOrg{OrgID: org, TimeZone: "Europe/Berlin", BriefingHour: 8})
	enq := &memEnq{}

	s1 := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}
	s1.Check(context.Background())
	s1.Check(context.Background()) // second tick same day
	if enq.count() != 1 {
		t.Fatalf("after two ticks: %d msgs", enq.count())
	}

	// Restart: new Scheduler, same store.
	s2 := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}
	s2.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("after restart: %d msgs", enq.count())
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			(&Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}).Check(context.Background())
		}()
	}
	wg.Wait()
	if enq.count() != 1 {
		t.Fatalf("concurrent schedulers: %d msgs", enq.count())
	}
	if st.claims() != 1 {
		t.Fatalf("claims=%d", st.claims())
	}

	body := enq.bodies()[0]
	if stringsCountLines(body) != 1 {
		t.Fatalf("quiet day body lines: %q", body)
	}
	msg := enq.msgs[0].Msg
	if msg.Kind != store.KindBriefing || len(msg.Actions) != 0 {
		t.Fatalf("message: %+v", msg)
	}
	if len(msg.Evidence) != 2 {
		t.Fatalf("evidence: %+v", msg.Evidence)
	}
}

func stringsCountLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestSchedulerDSTSpringAndFall(t *testing.T) {
	cases := []struct {
		name string
		tz   string
		// local civil dates around the transition
		dayBefore time.Time // noon local day before
		transDay  time.Time // the transition local date at briefing hour (or next valid)
		dayAfter  time.Time
		hour      int
	}{
		{
			name:      "Berlin spring",
			tz:        "Europe/Berlin",
			hour:      2, // 02:00 does not exist on spring-forward Sunday
			dayBefore: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
			transDay:  time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
			dayAfter:  time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
		},
		{
			name:      "Berlin fall",
			tz:        "Europe/Berlin",
			hour:      2, // 02:00 repeats
			dayBefore: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
			transDay:  time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
			dayAfter:  time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		},
		{
			name:      "New_York spring",
			tz:        "America/New_York",
			hour:      2,
			dayBefore: time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC),
			transDay:  time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
			dayAfter:  time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC),
		},
		{
			name:      "New_York fall",
			tz:        "America/New_York",
			hour:      1, // 01:00 repeats
			dayBefore: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
			transDay:  time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
			dayAfter:  time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.tz)
			if err != nil {
				t.Fatal(err)
			}
			org := uuid.New()
			st := newMemStore(store.BriefingOrg{OrgID: org, TimeZone: tc.tz, BriefingHour: tc.hour})
			enq := &memEnq{}
			clock := &memClock{}
			s := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}

			for _, noonUTC := range []time.Time{tc.dayBefore, tc.transDay, tc.dayAfter} {
				// Interpret the UTC placeholder as a civil date, then fire at
				// briefing hour (+30s) in the zone so catch-up applies.
				y, m, d := noonUTC.Date()
				target := time.Date(y, m, d, tc.hour, 0, 30, 0, loc)
				clock.Set(target)
				s.Check(context.Background())
				// Second pass in the repeated hour (fall-back): still one.
				clock.Set(target.Add(time.Hour))
				s.Check(context.Background())
			}
			if enq.count() != 3 {
				t.Fatalf("got %d briefings over 3 local days; want 3 (bodies=%v)", enq.count(), enq.bodies())
			}
		})
	}
}

func TestSchedulerDifferentTimeZones(t *testing.T) {
	orgUTC := uuid.New()
	orgNY := uuid.New()
	st := newMemStore(
		store.BriefingOrg{OrgID: orgUTC, TimeZone: "UTC", BriefingHour: 8},
		store.BriefingOrg{OrgID: orgNY, TimeZone: "America/New_York", BriefingHour: 8},
	)
	enq := &memEnq{}
	clock := &memClock{now: time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)}
	s := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}
	s.Check(context.Background())
	if enq.count() != 1 || enq.msgs[0].Org != orgUTC {
		t.Fatalf("at 08:00 UTC: %+v", enq.msgs)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 6, 15, 8, 0, 0, 0, ny))
	s.Check(context.Background())
	if enq.count() != 2 {
		t.Fatalf("after NY hour: %d", enq.count())
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range enq.msgs {
		seen[c.Org] = true
	}
	if !seen[orgUTC] || !seen[orgNY] {
		t.Fatalf("orgs=%v", seen)
	}
}

func TestSchedulerCatchUpWindow(t *testing.T) {
	org := uuid.New()
	st := newMemStore(store.BriefingOrg{OrgID: org, TimeZone: "UTC", BriefingHour: 8})
	enq := &memEnq{}
	clock := &memClock{now: time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)} // 6h late exactly
	s := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}
	s.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("at +6h: %d", enq.count())
	}

	org2 := uuid.New()
	st2 := newMemStore(store.BriefingOrg{OrgID: org2, TimeZone: "UTC", BriefingHour: 8})
	enq2 := &memEnq{}
	clock2 := &memClock{now: time.Date(2026, 6, 15, 14, 0, 1, 0, time.UTC)} // >6h
	s2 := &Scheduler{Store: st2, Enq: enq2, Clock: clock2, Log: quietLog()}
	s2.Check(context.Background())
	if enq2.count() != 0 {
		t.Fatalf("past window sent %d", enq2.count())
	}
}

func TestSchedulerFailedEnqueueRetried(t *testing.T) {
	org := uuid.New()
	st := newMemStore(store.BriefingOrg{OrgID: org, TimeZone: "UTC", BriefingHour: 8})
	enq := &memEnq{fail: 1}
	clock := &memClock{now: time.Date(2026, 6, 15, 8, 5, 0, 0, time.UTC)}
	s := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}
	s.Check(context.Background())
	if enq.count() != 0 {
		t.Fatal("first attempt should fail")
	}
	if st.claims() != 0 {
		t.Fatal("claim should be deleted after failure")
	}
	s.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("retry: %d", enq.count())
	}
}

func TestSchedulerBeforeHourNoSend(t *testing.T) {
	org := uuid.New()
	st := newMemStore(store.BriefingOrg{OrgID: org, TimeZone: "UTC", BriefingHour: 8})
	enq := &memEnq{}
	clock := &memClock{now: time.Date(2026, 6, 15, 7, 59, 0, 0, time.UTC)}
	(&Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog()}).Check(context.Background())
	if enq.count() != 0 {
		t.Fatal("sent before hour")
	}
}
