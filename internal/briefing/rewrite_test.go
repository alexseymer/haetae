package briefing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeRewriter struct {
	avail bool
	text  string
	err   error
	block chan struct{}
	calls int
	facts operator.BriefingFacts
}

func (f *fakeRewriter) Available(context.Context, uuid.UUID) (bool, string) { return f.avail, "" }
func (f *fakeRewriter) RewriteBriefing(ctx context.Context, _ uuid.UUID, facts operator.BriefingFacts, _ string) (string, error) {
	f.calls++
	f.facts = facts
	if f.block != nil {
		<-f.block // ignores ctx on purpose
	}
	return f.text, f.err
}

func rewriteFixture() (store.BriefingFacts, string, time.Time) {
	now := time.Date(2026, 6, 15, 8, 0, 30, 0, time.UTC)
	facts := store.BriefingFacts{
		Hosts: 2, Online: 2, Containers: map[string]int{"running": 5},
		Items: []store.BriefingItem{{Severity: "high", Title: "api crash-looping", HostName: "web-1", Kind: "incident", At: now}},
	}
	return facts, Compose(facts, now, time.UTC), now
}

func chooseWith(t *testing.T, rw *fakeRewriter, timeout time.Duration) (got, rule string) {
	t.Helper()
	facts, rule, _ := rewriteFixture()
	s := &Scheduler{Rewriter: rw, RewriteTimeout: timeout}
	return s.choose(context.Background(), uuid.New(), facts, rule), rule
}

func TestRewriteAccepted(t *testing.T) {
	rw := &fakeRewriter{avail: true, text: "08:00: 2 hosts, 5 containers, one problem.\nWatch api crash-looping on web-1."}
	got, rule := chooseWith(t, rw, time.Second)
	if got != rw.text || got == rule {
		t.Fatalf("got %q", got)
	}
	if rw.facts.Hosts != 2 || rw.facts.Containers != 5 || rw.facts.Running != 5 || len(rw.facts.Items) != 1 || rw.facts.Items[0] != "api crash-looping on web-1" {
		t.Fatalf("facts %+v", rw.facts)
	}
}

func TestRewriteRejected(t *testing.T) {
	cases := map[string]string{
		"too long":      "08:00: 2 hosts, 5 containers. api crash-looping on web-1 " + strings.Repeat("x", 300),
		"too many line": "08:00 2 hosts\n5 containers\napi crash-looping\nweb-1",
		"missing item":  "08:00: 2 hosts, 5 containers, one problem on web-1.",
		"missing host":  "08:00: 2 hosts, 5 containers, api crash-looping.",
		"wrong number":  "08:00: 12 hosts, 5 containers, api crash-looping on web-1.",
		"empty":         "  ",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			got, rule := chooseWith(t, &fakeRewriter{avail: true, text: text}, time.Second)
			if got != rule {
				t.Fatalf("accepted %q", got)
			}
		})
	}
}

func TestRewriteFallbacks(t *testing.T) {
	good := "08:00: 2 hosts, 5 containers. api crash-looping on web-1."
	if got, rule := chooseWith(t, &fakeRewriter{avail: true, err: errors.New("boom"), text: good}, time.Second); got != rule {
		t.Fatalf("error: %q", got)
	}
	if got, rule := chooseWith(t, &fakeRewriter{avail: false, text: good}, time.Second); got != rule {
		t.Fatalf("unavailable: %q", got)
	}
	start := time.Now()
	got, rule := chooseWith(t, &fakeRewriter{avail: true, text: good, block: make(chan struct{})}, 50*time.Millisecond)
	if got != rule || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: %q after %v", got, time.Since(start))
	}
}

func TestBriefingPreserves(t *testing.T) {
	rule := "08:00 briefing, 2 hosts, 12 containers, 1 not running.\n  db-1_x disk full on eu.host/1"
	ok := "08:00 2 hosts 12 containers 1 not running: db-1_x disk full on eu.host/1"
	if !briefingPreserves(rule, ok) {
		t.Fatal("should preserve")
	}
	if briefingPreserves(rule, strings.Replace(ok, "12 containers", "2 containers", 1)) {
		t.Fatal("12 must not be satisfied by 2")
	}
	if briefingPreserves(rule, strings.Replace(ok, "eu.host/1", "eu", 1)) {
		t.Fatal("missing host")
	}
}

func TestBriefingPreservesStateAndNoNewFacts(t *testing.T) {
	for _, tc := range []struct {
		name, rule, ai string
		want           bool
	}{
		{"inverted offline", "2 hosts offline", "2 hosts online", false},
		{"inverted not running", "3 not running", "all running", false},
		{"extra number", "2 hosts, 5 containers", "2 hosts, 5 containers, 7 alerts", false},
		{"new identifier", "api crash-looping", "api crash-looping on web-9", false},
		{"dropped critical", "critical: disk full", "disk full", false},
		{"faithful reword", "2 hosts offline, 3 not running", "Heads up: 2 hosts offline and 3 not running", true},
		{"same time and ids", "08:00 briefing, 2 hosts, db-1 disk full", "08:00: 2 hosts. db-1 disk full", true},
	} {
		if got := briefingPreserves(tc.rule, tc.ai); got != tc.want {
			t.Errorf("%s: briefingPreserves(%q, %q) = %v, want %v", tc.name, tc.rule, tc.ai, got, tc.want)
		}
	}
}

func TestBriefingSentOncePerDayWithAIBlocked(t *testing.T) {
	org := uuid.New()
	clock := &memClock{now: time.Date(2026, 6, 15, 8, 0, 30, 0, time.UTC)}
	st := newMemStore(store.BriefingOrg{OrgID: org, TimeZone: "UTC", BriefingHour: 8})
	enq := &memEnq{}
	rw := &fakeRewriter{avail: true, block: make(chan struct{})}
	s := &Scheduler{Store: st, Enq: enq, Clock: clock, Log: quietLog(), Rewriter: rw, RewriteTimeout: 50 * time.Millisecond}
	s.Check(context.Background())
	s.Check(context.Background())
	if enq.count() != 1 {
		t.Fatalf("%d briefings", enq.count())
	}
	if !strings.Contains(enq.bodies()[0], "briefing") {
		t.Fatalf("not the rule text: %q", enq.bodies()[0])
	}
}
