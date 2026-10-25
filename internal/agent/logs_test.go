package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/logpipe"
	"github.com/alexseymer/haetae/internal/redact"
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
	if r := dispatch(ctx, &agentv1.Command{Id: "1", Kind: &agentv1.Command_QueryLogs{QueryLogs: &agentv1.QueryLogs{}}}, RunOptions{Handler: handler}); r.GetCommandId() != "1" || r.GetError() != "" {
		t.Errorf("query logs: %+v", r)
	}
	if r := dispatch(ctx, &agentv1.Command{Id: "2"}, RunOptions{Handler: handler}); r.GetError() != "unsupported command" {
		t.Errorf("unknown kind must be refused: %+v", r)
	}
	if r := dispatch(ctx, &agentv1.Command{Id: "3", Kind: &agentv1.Command_QueryLogs{}}, RunOptions{}); r.GetError() != "unsupported command" {
		t.Errorf("with no handler everything but ping is refused: %+v", r)
	}
}

// A redaction timeout delays events; it must not lose warnings and errors.
func TestFlushKeepsEventsAfterRedactionTimeout(t *testing.T) {
	m, out, f := newTestLogManager(LogConfig{})
	addLines(f, "level=error msg=\"disk full\" volume=data")

	m.cfg.RedactionBudget = time.Nanosecond
	m.flush(context.Background())
	if out.Len() != 0 {
		t.Fatal("something was sent although redaction timed out")
	}
	if len(m.pendingItems) != 1 {
		t.Fatalf("pending = %d, want the error event kept", len(m.pendingItems))
	}

	m.cfg.RedactionBudget = 5 * time.Second
	m.flush(context.Background())
	msg, err := out.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evs := msg.GetLogs().GetEvents()
	if len(evs) != 1 || evs[0].GetLevel() != agentv1.LogLevel_LOG_LEVEL_ERROR {
		t.Fatalf("after the retry the error event must arrive, got %+v", evs)
	}
}

func TestPendingIsBoundedAndKeepsSevereFirst(t *testing.T) {
	m, _, _ := newTestLogManager(LogConfig{})
	var items []logItem
	for i := range maxPendingItems + 500 {
		lvl := logpipe.LevelInfo
		if i%10 == 0 {
			lvl = logpipe.LevelError
		}
		items = append(items, logItem{"c1", logpipe.Event{TemplateID: fmt.Sprint(i), Level: lvl, Count: 1, First: time.Unix(int64(i), 0)}})
	}
	m.keepPending(items, nil)
	if len(m.pendingItems) != maxPendingItems {
		t.Fatalf("pending = %d, want %d", len(m.pendingItems), maxPendingItems)
	}
	errs := 0
	for _, it := range m.pendingItems {
		if it.ev.Level == logpipe.LevelError {
			errs++
		}
	}
	if errs != (maxPendingItems+500+9)/10 {
		t.Fatalf("kept %d error events, want all %d", errs, (maxPendingItems+500+9)/10)
	}
	if m.dropped.Load() != 500 {
		t.Fatalf("dropped = %d, want 500 routine events counted", m.dropped.Load())
	}
}

// Batches stay within what the gateway accepts, so nothing is silently cut.
func TestSendChunksBatchesWithinGatewayLimits(t *testing.T) {
	m, out, _ := newTestLogManager(LogConfig{})
	var items []redactedItem
	for i := range 2500 {
		items = append(items, redactedItem{logItem: logItem{"c1", logpipe.Event{TemplateID: fmt.Sprint(i), Level: logpipe.LevelError, Count: 1}}})
	}
	var cands []redactedCand
	for i := range 200 {
		cands = append(cands, redactedCand{TemplateInfo: logpipe.TemplateInfo{ID: fmt.Sprint(i)}})
	}
	m.send(items, cands, logpipe.Stats{LinesIn: 7})

	var events, candidates, batches int
	var lastStats *agentv1.LogStats
	for out.Len() > 0 {
		msg, _ := out.Next(context.Background())
		b := msg.GetLogs()
		batches++
		if len(b.GetEvents()) > maxEventsPerBatch || len(b.GetCandidates()) > maxCandidatesPerBatch {
			t.Fatalf("batch of %d events / %d candidates exceeds the gateway limits", len(b.GetEvents()), len(b.GetCandidates()))
		}
		events += len(b.GetEvents())
		candidates += len(b.GetCandidates())
		if b.GetStats() != nil {
			lastStats = b.GetStats()
		}
	}
	if events != 2500 || candidates != 200 {
		t.Fatalf("sent %d events and %d candidates in %d batches, want 2500 and 200", events, candidates, batches)
	}
	if lastStats == nil || lastStats.GetLinesIn() != 7 {
		t.Fatalf("stats missing: %+v", lastStats)
	}
}

// The template and its ID come from redacted text: nothing a hash of the
// secret could be brute-forced from is sent.
func TestTemplateAndIDAreDerivedFromRedactedText(t *testing.T) {
	out := NewOutbox(4)
	m := NewLogManager(LogConfig{}, nil, out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	agg := logpipe.NewAggregator(logpipe.Config{MergeWindow: time.Millisecond, Canon: m.canonTemplate})
	line := "level=error login rejected for jane.doe@example.com"
	agg.Add(line, time.Now().Add(-time.Minute))
	ev := agg.Flush(true)[0]

	if strings.Contains(ev.Template, "jane.doe@example.com") {
		t.Fatalf("template carries the email: %q", ev.Template)
	}
	rawID := logpipe.TemplateID(ev.Level, logpipe.Template(line))
	if ev.TemplateID == rawID {
		t.Fatal("template ID is still the hash of the raw template")
	}
	if ev.TemplateID != logpipe.TemplateID(ev.Level, ev.Template) {
		t.Fatal("template ID is not the hash of the stored template")
	}
}

func TestSetGreenIsCapped(t *testing.T) {
	m, _, _ := newTestLogManager(LogConfig{})
	ids := make([]string, maxGreenIDs+5000)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	m.SetGreen(ids)
	if got := m.green.Load().Len(); got != maxGreenIDs {
		t.Fatalf("green set holds %d ids, want %d", got, maxGreenIDs)
	}
}

// A secret that straddles the sample's size limit must not leave a prefix
// behind: the line is redacted first, then cut.
func TestSampleIsRedactedBeforeItIsCut(t *testing.T) {
	out := NewOutbox(4)
	m := NewLogManager(LogConfig{}, nil, out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	agg := logpipe.NewAggregator(logpipe.Config{
		MergeWindow: time.Millisecond, MaxSampleLen: 64,
		Canon: m.canonTemplate, SampleCanon: m.canonTemplate,
	})
	secret := "gh" + "p_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"
	// Place the token so the 64-byte cut falls in the middle of it.
	line := "level=error msg=push failed " + strings.Repeat("x", 64-len("level=error msg=push failed ")-8) + " token=" + secret + " end"
	agg.Add(line, time.Now().Add(-time.Minute))
	ev := agg.Flush(true)[0]
	if strings.Contains(ev.Sample, "ghp_") || strings.Contains(ev.Sample, secret[:10]) {
		t.Fatalf("a prefix of the secret survived the cut: %q", ev.Sample)
	}
	if !strings.Contains(ev.Sample, "[REDACTED:") && len(ev.Sample) > 64 {
		t.Fatalf("unexpected sample: %q", ev.Sample)
	}
}

// If redaction fails for one event for a reason other than time, the others
// still go out and the failing one is dropped and counted: fail-closed per event.
func TestFlushIsolatesEventsThatCannotBeRedacted(t *testing.T) {
	m, out, f := newTestLogManager(LogConfig{})
	addLines(f,
		"level=error first disk is full",
		"level=error POISON network down",
		"level=error third queue stuck",
	)
	real := m.redactBatch
	m.redactBatch = func(ctx context.Context, lines []string, b time.Duration) (redact.Result, error) {
		for _, l := range lines {
			if strings.Contains(l, "POISON") {
				return redact.Result{}, errors.New("rule panicked")
			}
		}
		return real(ctx, lines, b)
	}
	m.flush(context.Background())

	msg, err := out.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var samples []string
	for _, e := range msg.GetLogs().GetEvents() {
		samples = append(samples, e.GetSample())
	}
	joined := strings.Join(samples, "|")
	if len(samples) != 2 || strings.Contains(joined, "POISON") || !strings.Contains(joined, "disk") || !strings.Contains(joined, "queue") {
		t.Fatalf("want the two healthy events and not the poisoned one, got %q", samples)
	}
	if m.redactFail.Load() == 0 || m.dropped.Load() == 0 {
		t.Fatalf("the dropped event was not counted: fail=%d dropped=%d", m.redactFail.Load(), m.dropped.Load())
	}
}

func TestTickWorkIsBoundedAndSevereFirst(t *testing.T) {
	m, _, _ := newTestLogManager(LogConfig{})
	var items []logItem
	for i := range 1500 {
		lvl := logpipe.LevelInfo
		if i >= 1400 { // the errors arrive last
			lvl = logpipe.LevelError
		}
		items = append(items, logItem{"c1", logpipe.Event{TemplateID: fmt.Sprint(i), Level: lvl, Count: 1, First: time.Unix(int64(i), 0)}})
	}
	now, _ := m.limitTick(items, nil)
	if len(now) != maxItemsPerTick {
		t.Fatalf("this tick redacts %d events, want %d", len(now), maxItemsPerTick)
	}
	errs := 0
	for _, it := range now {
		if it.ev.Level == logpipe.LevelError {
			errs++
		}
	}
	if errs != 100 {
		t.Fatalf("only %d of the 100 error events are in this tick", errs)
	}
	if len(m.pendingItems) != 1000 {
		t.Fatalf("pending = %d, want the other 1000 waiting", len(m.pendingItems))
	}
}

func TestKeepPendingMergesWithWaitingWork(t *testing.T) {
	m, _, _ := newTestLogManager(LogConfig{})
	m.pendingItems = []logItem{{"c1", logpipe.Event{TemplateID: "parked", Level: logpipe.LevelInfo, Count: 1}}}
	m.keepPending([]logItem{{"c1", logpipe.Event{TemplateID: "failed", Level: logpipe.LevelError, Count: 1}}}, nil)
	if len(m.pendingItems) != 2 || m.pendingItems[0].ev.TemplateID != "failed" {
		t.Fatalf("pending = %+v, want the failed work first and the parked work kept", m.pendingItems)
	}
}
