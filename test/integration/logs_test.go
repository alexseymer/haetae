//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/gateway"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

// startLogAgent enrolls and runs an agent with log collection and short
// windows so the test needs seconds, not minutes. Production defaults are
// 10 s windows and 10 s judging.
func startLogAgent(t *testing.T, e *env, name string, logCfg agent.LogConfig) (hostID uuid.UUID, logs *agent.LogManager) {
	t.Helper()
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token(name), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := agent.NewOutbox(30)
	mon := agent.NewMonitor(agent.MonitorConfig{Interval: time.Second}, out, e.log.With("component", "monitor"))
	logs = agent.NewLogManager(logCfg, mon.Docker(), out, e.log.With("component", "logs"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mon.OnRunning = func(r map[string]string) { logs.Sync(ctx, r) }
	go logs.Run(ctx)
	e.runAgentWith(id, agent.RunOptions{
		Outbox: out, OnConnected: mon.Resync, DockerVersion: mon.DockerVersion,
		OnGreen: logs.SetGreen, Handler: logs.Handle,
	}, mon)
	return uuid.MustParse(id.HostID), logs
}

// S5 and S6 end to end on a real container: every warning and error reaches
// the server, identical lines are merged with a count, routine lines turn
// green and stop being sent, and no secret or personal data leaves the host.
func TestLogPipelineEndToEnd(t *testing.T) {
	requireDocker(t)
	rec := &recorder{}
	e := newEnvWith(t, func(c *gateway.Config) {
		c.Tap = rec.tap
		c.JudgeEvery = time.Second
	})

	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	canaryPW := "pw" + hex.EncodeToString(raw) // secret in a key=value
	canaryMail := "u" + hex.EncodeToString(raw[:4]) + "@canary-example.com"
	name := "haetae-logs-" + hex.EncodeToString(raw[:4])

	// Waits 5 s so the agent follows the container before its first line.
	// Per loop (about 20 ms): one routine info line; every 100th an error that
	// carries the canaries; every 50th a warning.
	script := strings.NewReplacer("@@MAIL@@", canaryMail, "@@PW@@", canaryPW).Replace(`sleep 5; i=0; while [ $i -lt 2000 ]; do i=$((i+1));
 echo "GET /api/items/$i 200 $((i%50))ms level=info";
 if [ $((i%100)) -eq 0 ]; then echo "level=error msg=\"db connection failed\" user=@@MAIL@@ password=@@PW@@ attempt=$i"; fi;
 if [ $((i%50)) -eq 0 ]; then echo "level=warn msg=\"queue depth high\" depth=$((i*3))"; fi;
 usleep 20000; done; sleep 600`)
	docker(t, "pull", "-q", "busybox:1.37")
	cid := docker(t, "run", "-d", "--rm", "--name", name, "--log-opt", "max-size=5m",
		"busybox:1.37", "sh", "-c", script)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", cid).Run() })

	agentStart := time.Now()
	hostID, _ := startLogAgent(t, e, "logs-host", agent.LogConfig{
		FlushEvery: 500 * time.Millisecond, MergeWindow: time.Second,
	})
	ctx := context.Background()

	// 1. The routine info template turns green and the agent is told.
	var greenAt time.Time
	waitFor(t, 60*time.Second, "info template judged green", func() bool {
		ids, err := e.st.GreenTemplateIDs(ctx, e.org.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) > 0 && greenAt.IsZero() {
			greenAt = time.Now()
		}
		return len(ids) > 0
	})
	t.Logf("MEASURE S5 first template judged green %s after the agent started (windows 1 s, judging every 1 s)", greenAt.Sub(agentStart).Round(time.Second))

	// 2. The skipping starts: the agent's counters show skipped lines.
	waitFor(t, 30*time.Second, "agent skipping green lines", func() bool {
		st, err := e.st.GetLogStats(ctx, e.org.ID, hostID)
		return err == nil && st.LinesSkipped > 0
	})

	// 3. Let the container finish its 2000 iterations (about 45 s of output).
	waitFor(t, 120*time.Second, "container finished", func() bool {
		return strings.Contains(docker(t, "logs", "--tail", "5", cid), "level=") &&
			countLines(docker(t, "logs", cid), "GET /api/items/2000 ") == 1
	})
	allLogs := docker(t, "logs", "--timestamps", cid)
	producedErr := countLines(allLogs, "level=error")
	producedWarn := countLines(allLogs, "level=warn")
	producedInfo := countLines(allLogs, "level=info")
	if producedErr != 20 || producedWarn != 40 {
		t.Fatalf("container produced %d errors and %d warnings, want 20 and 40", producedErr, producedWarn)
	}

	// 4. Every warning and error is on the server (sum of merged counts).
	sum := func(minLevel, maxLevel int16) (n int) {
		evs, err := e.st.ListLogEvents(ctx, e.org.ID, hostID, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), minLevel, 100000)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Level <= maxLevel {
				n += ev.Count
			}
		}
		return n
	}
	waitFor(t, 30*time.Second, "all warnings and errors stored", func() bool {
		return sum(store.LevelError, store.LevelFatal) == producedErr && sum(store.LevelWarn, store.LevelWarn) == producedWarn
	})
	t.Logf("MEASURE S5 warn/error lines produced=%d stored=%d", producedErr+producedWarn,
		sum(store.LevelError, store.LevelFatal)+sum(store.LevelWarn, store.LevelWarn))

	// 5. Merging: the stored info events carry counts, far fewer events than lines.
	infoEvents, err := e.st.ListLogEvents(ctx, e.org.ID, hostID, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), store.LevelInfo, 100000)
	if err != nil {
		t.Fatal(err)
	}
	infoSent, infoEventsN, maxCount := 0, 0, 0
	for _, ev := range infoEvents {
		if ev.Level == store.LevelInfo {
			infoSent += ev.Count
			infoEventsN++
			maxCount = max(maxCount, ev.Count)
		}
	}
	if maxCount < 10 {
		t.Errorf("largest merged info event has count %d; identical lines are not being merged", maxCount)
	}

	// 6. After green, routine lines are no longer sent.
	sentAfter := 0
	for _, ev := range infoEvents {
		if ev.Level == store.LevelInfo && ev.FirstTS.After(greenAt.Add(3*time.Second)) {
			sentAfter += ev.Count
		}
	}
	producedAfter := countAfter(allLogs, "level=info", greenAt.Add(3*time.Second))
	if producedAfter < 200 {
		t.Fatalf("only %d info lines were produced after the green point; the container finished too early to measure", producedAfter)
	}
	neverSent := 1 - float64(sentAfter)/float64(producedAfter)
	t.Logf("MEASURE S5 after green: info lines produced=%d sent=%d never-sent=%.3f (target >= 0.95); whole run: %d info lines in %d events (largest count %d)",
		producedAfter, sentAfter, neverSent, producedInfo, infoEventsN, maxCount)
	if neverSent < 0.95 {
		t.Errorf("only %.1f%% of routine lines were withheld after green, want >= 95%%", 100*neverSent)
	}

	// 7. Nothing sensitive left the host: not on the wire, not in the database.
	for _, canary := range []string{canaryPW, canaryMail} {
		if n, leaked := rec.contains([]byte(canary)); leaked {
			t.Errorf("%q reached the server on the wire (%d messages checked)", canary, n)
		}
		var found int
		if err := e.st.Pool().QueryRow(ctx,
			`SELECT (SELECT count(*) FROM log_events WHERE sample LIKE '%'||$1||'%')
			      + (SELECT count(*) FROM log_templates WHERE sample LIKE '%'||$1||'%' OR template LIKE '%'||$1||'%')`, canary).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 0 {
			t.Errorf("%q is stored on the server in %d rows", canary, found)
		}
	}
	var redacted int
	_ = e.st.Pool().QueryRow(ctx, `SELECT count(*) FROM log_events WHERE sample LIKE '%[REDACTED:%'`).Scan(&redacted)
	if redacted == 0 {
		t.Error("no stored sample shows a redaction marker; the error lines should have been redacted, not dropped")
	}

	// 8. On-demand query: recent lines come back redacted.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := e.gw.Send(cctx, hostID, &agentv1.Command{Kind: &agentv1.Command_QueryLogs{QueryLogs: &agentv1.QueryLogs{
		ContainerId: cid, FromUnixMs: time.Now().Add(-10 * time.Minute).UnixMilli(), MaxLines: 500,
	}}})
	if err != nil || res.GetError() != "" {
		t.Fatalf("query logs: res=%v err=%v", res, err)
	}
	var sawError bool
	for _, l := range res.GetLogLines().GetLines() {
		if strings.Contains(l.GetLine(), canaryPW) || strings.Contains(l.GetLine(), canaryMail) {
			t.Fatalf("on-demand log line leaked a canary: %s", l.GetLine())
		}
		if strings.Contains(l.GetLine(), "db connection failed") {
			sawError = true
		}
	}
	if !sawError || len(res.GetLogLines().GetLines()) == 0 {
		t.Errorf("on-demand query returned %d lines, expected the recent error lines", len(res.GetLogLines().GetLines()))
	}
}

func countLines(s, sub string) (n int) {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// countAfter counts lines containing sub whose docker timestamp is after t.
func countAfter(timestamped, sub string, t time.Time) (n int) {
	for _, l := range strings.Split(timestamped, "\n") {
		if !strings.Contains(l, sub) {
			continue
		}
		ts, _, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil && parsed.After(t) {
			n++
		}
	}
	return n
}
