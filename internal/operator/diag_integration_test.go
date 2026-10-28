//go:build integration

package operator_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/operator/provider"
)

// recHandler records every log record.
type recHandler struct {
	mu   sync.Mutex
	recs []map[string]any
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recHandler) WithGroup(string) slog.Handler            { return h }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"level": r.Level, "msg": r.Message}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	h.mu.Lock()
	h.recs = append(h.recs, m)
	h.mu.Unlock()
	return nil
}
func (h *recHandler) all() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.recs...)
}

func failing(err error) *provider.Fake {
	return &provider.Fake{IsLocal: true, ModelID: "vendor/model", Script: func(int, provider.Request) (provider.Response, error) {
		return provider.Response{}, err
	}}
}

func TestFailureLogIsClassedAndRateLimited(t *testing.T) {
	e := newEnv(t)
	h := &recHandler{}
	now := time.Now()
	svc := e.svc(failing(&provider.Error{Kind: provider.ErrBadResponse, Status: 404, Detail: "No endpoints found"}), func(c *operator.Config) {
		c.Log = slog.New(h)
		c.Now = func() time.Time { return now }
	})
	const secret = "my-secret-question-text"
	for i := 0; i < 5; i++ {
		_, _ = svc.Ask(context.Background(), e.org.ID, secret)
	}
	recs := h.all()
	if len(recs) != 1 {
		t.Fatalf("want 1 log line for 5 failures, got %d: %v", len(recs), recs)
	}
	r := recs[0]
	if r["level"] != slog.LevelWarn || r["class"] != "bad_request" || r["status"] != int64(404) ||
		r["detail"] != "No endpoints found" || r["provider"] != "fake" || r["model"] != "vendor/model" {
		t.Fatalf("record = %v", r)
	}
	for _, v := range r {
		if s, ok := v.(string); ok && strings.Contains(s, secret) {
			t.Fatalf("question leaked into log: %v", r)
		}
	}
	// A different class is logged at once; the same class again after 60 s.
	svc2 := e.svc(failing(&provider.Error{Kind: provider.ErrAuth, Status: 401}), func(c *operator.Config) {
		c.Log = slog.New(h)
		c.Now = func() time.Time { return now }
	})
	_, _ = svc2.Ask(context.Background(), e.org.ID, "q")
	now = now.Add(61 * time.Second)
	_, _ = svc2.Ask(context.Background(), e.org.ID, "q")
	if n := len(h.all()); n != 3 {
		t.Fatalf("records = %d, want 3", n)
	}
}

func TestTestProviderReportsToolSupport(t *testing.T) {
	e := newEnv(t)
	e.settings(false, "report", 1) // disabled and capped: the test still runs
	f := &provider.Fake{IsLocal: true, ModelID: "vendor/model", Script: func(n int, req provider.Request) (provider.Response, error) {
		if n == 1 {
			return provider.Response{ToolCalls: []provider.ToolCall{{Name: "ping"}}, Usage: provider.Usage{InputTokens: 5, OutputTokens: 2}}, nil
		}
		return provider.Response{Text: "OK", Usage: provider.Usage{InputTokens: 5, OutputTokens: 1}}, nil
	}}
	res, err := e.svc(f).TestProvider(context.Background(), e.org.ID)
	if err != nil || !res.OK || res.ToolCalling != operator.ToolsSupported || res.Provider != "fake" || res.Model != "vendor/model" || !res.Local {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 || reqs[0].MaxTokens != 16 || len(reqs[0].Tools) != 0 || len(reqs[1].Tools) != 1 || reqs[1].Tools[0].Name != "ping" {
		t.Fatalf("requests = %+v", reqs)
	}
	u, _ := e.s.UsageToday(context.Background(), e.org.ID, time.Now())
	if u.Total() != 13 {
		t.Fatalf("usage = %d, want 13", u.Total())
	}

	// A model that ignores the tool.
	f2 := &provider.Fake{IsLocal: true, Responses: []provider.Response{{Text: "OK"}, {Text: "pong"}}}
	res, _ = e.svc(f2).TestProvider(context.Background(), e.org.ID)
	if !res.OK || res.ToolCalling != operator.ToolsNotUsed {
		t.Fatalf("res=%+v", res)
	}

	// A provider that refuses the tools request.
	f3 := &provider.Fake{IsLocal: true, Script: func(n int, _ provider.Request) (provider.Response, error) {
		if n == 0 {
			return provider.Response{Text: "OK"}, nil
		}
		return provider.Response{}, &provider.Error{Kind: provider.ErrBadResponse, Status: 404, Detail: "no tool support"}
	}}
	res, _ = e.svc(f3).TestProvider(context.Background(), e.org.ID)
	if !res.OK || res.ToolCalling != operator.ToolsNotUsed || !strings.Contains(res.Reason, "no tool support") {
		t.Fatalf("res=%+v", res)
	}
}

func TestTestProviderFailureAndNoProvider(t *testing.T) {
	e := newEnv(t)
	res, err := e.svc(failing(&provider.Error{Kind: provider.ErrAuth, Status: 401, Detail: "x"})).TestProvider(context.Background(), e.org.ID)
	if err != nil || res.OK || res.Class != "auth" || res.Reason != "the provider rejected the API key (HTTP 401)" || res.Hint == "" || res.ToolCalling != operator.ToolsUnknown {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := operator.New(operator.Config{Store: e.s}).TestProvider(context.Background(), e.org.ID); err == nil {
		t.Fatal("want an error without a provider")
	}
}
