package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

type judgeFakeStore struct {
	mu           sync.Mutex
	ts           []store.LogTemplate
	green, watch []string
	greenSet     map[string]bool
	judged       map[string]bool
}

func (f *judgeFakeStore) JudgeableTemplates(context.Context, uuid.UUID, uuid.UUID, int64, int, int) ([]store.LogTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.LogTemplate
	for _, t := range f.ts {
		if !f.judged[t.TemplateID] {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *judgeFakeStore) SetVerdicts(_ context.Context, _, _ uuid.UUID, green, watch []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Like the real store, only unjudged templates change.
	if f.judged == nil {
		f.judged = map[string]bool{}
		f.greenSet = map[string]bool{}
	}
	for _, g := range green {
		if !f.judged[g] {
			f.judged[g] = true
			f.greenSet[g] = true
			f.green = append(f.green, g)
		}
	}
	for _, w := range watch {
		if !f.judged[w] {
			f.judged[w] = true
			f.watch = append(f.watch, w)
		}
	}
	sort.Strings(f.green)
	sort.Strings(f.watch)
	return nil
}
func (f *judgeFakeStore) DemoteGreen(_ context.Context, _, _ uuid.UUID, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		if f.greenSet[id] {
			delete(f.greenSet, id)
			f.watch = append(f.watch, id)
		}
	}
	sort.Strings(f.watch)
	return nil
}
func (f *judgeFakeStore) GreenTemplateIDs(context.Context, uuid.UUID, uuid.UUID, int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.greenSet {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

type fakeAIJudge struct {
	avail bool
	err   error
	block bool
	flag  map[string]bool // template id -> Green=false
	got   []operator.TemplateQuery
	calls atomic.Int32
	gate  chan struct{} // when set, JudgeTemplates waits for it to close
}

func (f *fakeAIJudge) Available(context.Context, uuid.UUID) (bool, string) { return f.avail, "" }
func (f *fakeAIJudge) JudgeTemplates(_ context.Context, _ uuid.UUID, ts []operator.TemplateQuery) ([]operator.Verdict, error) {
	f.got = ts
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if f.block {
		select {}
	}
	if f.err != nil {
		return nil, f.err
	}
	var out []operator.Verdict
	for _, t := range ts {
		out = append(out, operator.Verdict{TemplateID: t.TemplateID, Green: !f.flag[t.TemplateID]})
	}
	// A hostile verdict for a template that was never sent must be ignored.
	out = append(out, operator.Verdict{TemplateID: "err1", Green: true})
	return out, nil
}

func judgeFixture() *judgeFakeStore {
	return &judgeFakeStore{ts: []store.LogTemplate{
		{TemplateID: "info1", Level: store.LevelInfo, Template: "GET /x <NUM>", Occurrences: 50},
		{TemplateID: "info2", Level: store.LevelInfo, Template: "cache hit <STR>", Occurrences: 50},
		{TemplateID: "sus1", Level: store.LevelInfo, Template: "request failed", Occurrences: 50},
		{TemplateID: "err1", Level: store.LevelError, Template: "GET /x <NUM>", Occurrences: 50},
		{TemplateID: "warn1", Level: store.LevelWarn, Template: "all fine", Occurrences: 50},
	}}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// gwStore adapts the fake to the gateway Store; only the judge methods work.
type gwStore struct {
	Store
	f *judgeFakeStore
}

func (g *gwStore) JudgeableTemplates(ctx context.Context, o, h uuid.UUID, a int64, b, c int) ([]store.LogTemplate, error) {
	return g.f.JudgeableTemplates(ctx, o, h, a, b, c)
}
func (g *gwStore) SetVerdicts(ctx context.Context, o, h uuid.UUID, gr, w []string) error {
	return g.f.SetVerdicts(ctx, o, h, gr, w)
}
func (g *gwStore) DemoteGreen(ctx context.Context, o, h uuid.UUID, ids []string) error {
	return g.f.DemoteGreen(ctx, o, h, ids)
}
func (g *gwStore) GreenTemplateIDs(ctx context.Context, o, h uuid.UUID, n int) ([]string, error) {
	return g.f.GreenTemplateIDs(ctx, o, h, n)
}

func judgeGateway(t *testing.T, ai AIJudge) (*Gateway, *gwStore, *session) {
	t.Helper()
	SetAIJudge(ai)
	t.Cleanup(func() { SetAIJudge(nil) })
	st := &gwStore{f: judgeFixture()}
	g := New(st, nil, Config{}, slog.New(slog.DiscardHandler))
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()
	return g, st, s
}

// nextGreen waits for the next pushed green set.
func nextGreen(t *testing.T, s *session, d time.Duration) []string {
	t.Helper()
	select {
	case ids := <-s.green:
		return ids
	case <-time.After(d):
		t.Fatal("no green set pushed")
		return nil
	}
}

func TestJudgeHostRuleOnlyNeverCallsAI(t *testing.T) {
	ai := &fakeAIJudge{avail: true, flag: map[string]bool{"info1": true}}
	SetAIJudge(ai)
	t.Cleanup(func() { SetAIJudge(nil) })
	st := judgeFixture()
	green, err := judgeHost(context.Background(), st, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(green) != 2 || !eq(st.green, []string{"info1", "info2"}) || !eq(st.watch, []string{"err1", "sus1", "warn1"}) {
		t.Fatalf("green=%v watch=%v", st.green, st.watch)
	}
	if ai.calls.Load() != 0 {
		t.Fatal("judgeHost called the AI")
	}
}

func TestIngestJudgeDoesNotWaitForHungAI(t *testing.T) {
	ai := &fakeAIJudge{avail: true, block: true}
	g, st, s := judgeGateway(t, ai)
	start := time.Now()
	g.judgeAndPush(context.Background(), s)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("judgeAndPush took %v with a hung AI", d)
	}
	if got := nextGreen(t, s, time.Second); !eq(got, []string{"info1", "info2"}) {
		t.Fatalf("pushed %v", got)
	}
	if !eq(st.f.green, []string{"info1", "info2"}) {
		t.Fatalf("rule green = %v", st.f.green)
	}
}

func TestAIPassOnePerHostAndDemotionPushesSmallerSet(t *testing.T) {
	ai := &fakeAIJudge{avail: true, gate: make(chan struct{}), flag: map[string]bool{"info2": true}}
	g, _, s := judgeGateway(t, ai)
	g.judgeAndPush(context.Background(), s)
	if got := nextGreen(t, s, time.Second); !eq(got, []string{"info1", "info2"}) {
		t.Fatalf("pushed %v", got)
	}
	// Wait until the pass is inside the AI, then ingest again.
	deadline := time.Now().Add(time.Second)
	for ai.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	g.startAIPass(s, judgeFixture().ts[:2])
	time.Sleep(50 * time.Millisecond)
	if n := ai.calls.Load(); n != 1 {
		t.Fatalf("AI calls during in-flight pass = %d, want 1", n)
	}
	close(ai.gate)
	if got := nextGreen(t, s, 2*time.Second); !eq(got, []string{"info1"}) {
		t.Fatalf("after demotion pushed %v, want [info1]", got)
	}
}

func TestAIPassErrorKeepsRuleVerdicts(t *testing.T) {
	for name, ai := range map[string]*fakeAIJudge{
		"error":       {avail: true, err: errors.New("boom"), flag: map[string]bool{"info1": true}},
		"unavailable": {avail: false, flag: map[string]bool{"info1": true}},
	} {
		t.Run(name, func(t *testing.T) {
			g, st, s := judgeGateway(t, ai)
			g.judgeAndPush(context.Background(), s)
			nextGreen(t, s, time.Second)
			time.Sleep(100 * time.Millisecond)
			if got, _ := st.GreenTemplateIDs(context.Background(), s.orgID, s.hostID, 10); !eq(got, []string{"info1", "info2"}) {
				t.Fatalf("green=%v", got)
			}
			select {
			case ids := <-s.green:
				t.Fatalf("unexpected second push %v", ids)
			default:
			}
		})
	}
}

func TestAIDemotionsOnlyDemotesCandidates(t *testing.T) {
	ai := &fakeAIJudge{avail: true, flag: map[string]bool{"info2": true, "err1": true}}
	cand := []store.LogTemplate{
		{TemplateID: "info1", Level: store.LevelInfo, Template: "a"},
		{TemplateID: "info2", Level: store.LevelInfo, Template: "b"},
	}
	got := aiDemotions(context.Background(), ai, uuid.New(), cand)
	if !eq(got, []string{"info2"}) {
		t.Fatalf("demotions = %v", got)
	}
	for _, q := range ai.got {
		if q.TemplateID != "info1" && q.TemplateID != "info2" {
			t.Fatalf("AI was sent %q", q.TemplateID)
		}
	}
}
