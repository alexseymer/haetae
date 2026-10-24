package agent

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/logpipe"
)

// newTestLogManager returns a manager with one pre-populated follower, no Docker.
func newTestLogManager(cfg LogConfig) (*LogManager, *Outbox, *follower) {
	out := NewOutbox(4)
	m := NewLogManager(cfg, nil, out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := &follower{
		name: "web",
		agg:  logpipe.NewAggregator(logpipe.Config{MergeWindow: time.Millisecond}),
		ring: logpipe.NewRing(100, 64<<10),
	}
	m.followers["c1"] = f
	return m, out, f
}

func addLines(f *follower, lines ...string) {
	t := time.Now().Add(-time.Minute) // old enough that every window has ended
	for _, l := range lines {
		f.ring.Add(t, l)
		f.agg.Add(l, t)
	}
}

func TestFlushSendsRedactedMergedEvents(t *testing.T) {
	m, out, f := newTestLogManager(LogConfig{})
	addLines(f,
		"level=error msg=\"login failed\" email=jane@example.com password=hunter2hunter2",
		"level=error msg=\"login failed\" email=jane@example.com password=hunter2hunter2",
	)
	m.flush(context.Background())

	msg, err := out.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	batch := msg.GetLogs()
	if len(batch.GetEvents()) != 1 || batch.GetEvents()[0].GetCount() != 2 {
		t.Fatalf("want one merged event of count 2, got %+v", batch.GetEvents())
	}
	b := batch.String()
	for _, secret := range []string{"jane@example.com", "hunter2hunter2"} {
		if strings.Contains(b, secret) {
			t.Fatalf("%q left the host: %s", secret, b)
		}
	}
	if batch.GetEvents()[0].GetLevel() != agentv1.LogLevel_LOG_LEVEL_ERROR {
		t.Errorf("level = %v, want error", batch.GetEvents()[0].GetLevel())
	}
}

// S6: if redaction fails, nothing from the batch is sent.
func TestFlushFailsClosedWhenRedactionFails(t *testing.T) {
	m, out, f := newTestLogManager(LogConfig{RedactionBudget: time.Nanosecond})
	addLines(f, "level=error msg=boom token=ghp"+"_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")
	m.flush(context.Background())

	if out.Len() != 0 {
		t.Fatalf("outbox holds %d messages after a failed redaction, want 0", out.Len())
	}
	if m.redactFail.Load() != 1 {
		t.Fatalf("redaction failures = %d, want 1", m.redactFail.Load())
	}
}

func TestQueryLogsRedactsAndFailsClosed(t *testing.T) {
	m, _, f := newTestLogManager(LogConfig{})
	addLines(f, "user jane@example.com logged in", "plain line")

	res := m.QueryLogs(context.Background(), &agentv1.QueryLogs{ContainerId: "c1", MaxLines: 10})
	if res.GetError() != "" {
		t.Fatal(res.GetError())
	}
	lines := res.GetLogLines().GetLines()
	if len(lines) != 2 || strings.Contains(lines[0].GetLine(), "jane@example.com") {
		t.Fatalf("lines = %v", lines)
	}

	if r := m.QueryLogs(context.Background(), &agentv1.QueryLogs{ContainerId: "nope"}); r.GetError() == "" {
		t.Error("unknown container must return an error")
	}

	failing, _, ff := newTestLogManager(LogConfig{RedactionBudget: time.Nanosecond})
	addLines(ff, "secret password=hunter2hunter2")
	r := failing.QueryLogs(context.Background(), &agentv1.QueryLogs{ContainerId: "c1", MaxLines: 10})
	if r.GetError() == "" || r.GetLogLines() != nil {
		t.Fatalf("failed redaction must return an error and no lines: %+v", r)
	}
}

func TestOutboxBoundsLogBatches(t *testing.T) {
	o := NewOutbox(2)
	dropped := 0
	for range maxLogBatches + 5 {
		dropped += o.PutLogs(&agentv1.LogBatch{Events: []*agentv1.LogEvent{{TemplateId: "x"}}})
	}
	if o.Len() != maxLogBatches || dropped != 5 {
		t.Fatalf("Len=%d dropped=%d, want %d and 5", o.Len(), dropped, maxLogBatches)
	}
	// Logs go out before metrics.
	o.PutMetrics(&agentv1.MetricsBatch{})
	msg, _ := o.Next(context.Background())
	if msg.GetLogs() == nil {
		t.Fatalf("first message = %v, want logs", msg)
	}
}

func TestDispatchAllowlist(t *testing.T) {
	handler := func(_ context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
		if cmd.GetQueryLogs() != nil {
			return &agentv1.CommandResult{}
		}
		return nil
	}
	ctx := context.Background()
	if r := dispatch(ctx, &agentv1.Command{Id: "1", Kind: &agentv1.Command_QueryLogs{QueryLogs: &agentv1.QueryLogs{}}}, handler); r.GetCommandId() != "1" || r.GetError() != "" {
		t.Errorf("query logs: %+v", r)
	}
	if r := dispatch(ctx, &agentv1.Command{Id: "2"}, handler); r.GetError() != "unsupported command" {
		t.Errorf("unknown kind must be refused: %+v", r)
	}
	if r := dispatch(ctx, &agentv1.Command{Id: "3", Kind: &agentv1.Command_QueryLogs{}}, nil); r.GetError() != "unsupported command" {
		t.Errorf("with no handler everything but ping is refused: %+v", r)
	}
}
