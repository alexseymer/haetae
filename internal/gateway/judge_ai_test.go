package gateway

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

type judgeFakeStore struct {
	ts           []store.LogTemplate
	green, watch []string
}

func (f *judgeFakeStore) JudgeableTemplates(context.Context, uuid.UUID, uuid.UUID, int64, int, int) ([]store.LogTemplate, error) {
	return f.ts, nil
}
func (f *judgeFakeStore) SetVerdicts(_ context.Context, _, _ uuid.UUID, green, watch []string) error {
	f.green, f.watch = green, watch
	sort.Strings(f.green)
	sort.Strings(f.watch)
	return nil
}
func (f *judgeFakeStore) GreenTemplateIDs(context.Context, uuid.UUID, uuid.UUID, int) ([]string, error) {
	return nil, nil
}

type fakeAIJudge struct {
	avail bool
	err   error
	block bool
	flag  map[string]bool // template id -> Green=false
	got   []operator.TemplateQuery
}

func (f *fakeAIJudge) Available(context.Context, uuid.UUID) (bool, string) { return f.avail, "" }
func (f *fakeAIJudge) JudgeTemplates(_ context.Context, _ uuid.UUID, ts []operator.TemplateQuery) ([]operator.Verdict, error) {
	f.got = ts
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

func runJudge(t *testing.T, ai AIJudge) *judgeFakeStore {
	t.Helper()
	SetAIJudge(ai)
	t.Cleanup(func() { SetAIJudge(nil) })
	st := judgeFixture()
	if _, err := judgeHost(context.Background(), st, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	return st
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

func TestJudgeNoAIUsesRule(t *testing.T) {
	st := runJudge(t, nil)
	if !eq(st.green, []string{"info1", "info2"}) || !eq(st.watch, []string{"err1", "sus1", "warn1"}) {
		t.Fatalf("green=%v watch=%v", st.green, st.watch)
	}
}

func TestJudgeAICanOnlyDemoteAndNeverSeesRiskyLevels(t *testing.T) {
	ai := &fakeAIJudge{avail: true, flag: map[string]bool{"info2": true}}
	st := runJudge(t, ai)
	if !eq(st.green, []string{"info1"}) || !eq(st.watch, []string{"err1", "info2", "sus1", "warn1"}) {
		t.Fatalf("green=%v watch=%v", st.green, st.watch)
	}
	for _, q := range ai.got {
		if q.TemplateID != "info1" && q.TemplateID != "info2" {
			t.Fatalf("AI was sent %q", q.TemplateID)
		}
	}
	// The AI said Green for err1 (never sent) and sus1 is not even asked: neither is green.
	for _, g := range st.green {
		if g == "err1" || g == "sus1" || g == "warn1" {
			t.Fatalf("%s became green", g)
		}
	}
}

func TestJudgeAIFailureFallsBackToRule(t *testing.T) {
	for name, ai := range map[string]*fakeAIJudge{
		"error":       {avail: true, err: errors.New("boom"), flag: map[string]bool{"info1": true}},
		"unavailable": {avail: false, flag: map[string]bool{"info1": true}},
	} {
		t.Run(name, func(t *testing.T) {
			st := runJudge(t, ai)
			if !eq(st.green, []string{"info1", "info2"}) {
				t.Fatalf("green=%v", st.green)
			}
		})
	}
}

func TestJudgeAIHangFallsBackToRule(t *testing.T) {
	SetAIJudge(&fakeAIJudge{avail: true, block: true})
	t.Cleanup(func() { SetAIJudge(nil) })
	st := judgeFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := judgeHost(ctx, st, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if !eq(st.green, []string{"info1", "info2"}) {
		t.Fatalf("green=%v", st.green)
	}
}
