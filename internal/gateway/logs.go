package gateway

import (
	"context"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

// Limits on one log batch, applied after the message-size limit.
const (
	maxLogEventsPerBatch  = 1000
	maxCandidatesPerBatch = 64
	maxSampleBytes        = 1024
	maxTemplateBytes      = 512
	maxEventCount         = 1 << 30
)

// logStore is the part of Store used for logs.
type logStore interface {
	InsertLogEvents(ctx context.Context, orgID, hostID uuid.UUID, evs []store.LogEvent) error
	UpsertTemplateCandidates(ctx context.Context, orgID, hostID uuid.UUID, ts []store.LogTemplate) error
	SetLogStats(ctx context.Context, orgID, hostID uuid.UUID, st store.LogStats) error
	JudgeStore
}

func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !runeStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }

// storedLevel maps the wire level to the stored one; anything out of range
// becomes 0 (unknown), which is never judged routine.
func storedLevel(l agentv1.LogLevel) int16 {
	if l < agentv1.LogLevel_LOG_LEVEL_TRACE || l > agentv1.LogLevel_LOG_LEVEL_FATAL {
		return 0
	}
	return int16(l) //nolint:gosec // G115: checked to be 1..6
}

func (g *Gateway) ingestLogs(ctx context.Context, s *session, b *agentv1.LogBatch) {
	now := time.Now()
	clamp := func(ms int64) time.Time {
		t := time.UnixMilli(ms).UTC()
		if !sampleTimeOK(t, now) {
			return now.UTC()
		}
		return t
	}

	evs := b.GetEvents()
	if len(evs) > maxLogEventsPerBatch {
		// Agents split their batches below this limit; reaching it means a
		// misbehaving agent, which must not go unnoticed.
		g.log.Warn("log batch truncated", "host_id", s.hostID, "events", len(evs), "kept", maxLogEventsPerBatch)
		evs = evs[:maxLogEventsPerBatch]
	}
	events := make([]store.LogEvent, 0, len(evs))
	for _, e := range evs {
		if e.GetTemplateId() == "" || e.GetCount() == 0 {
			continue
		}
		events = append(events, store.LogEvent{
			ContainerID: clip(e.GetContainerId()),
			TemplateID:  clip(e.GetTemplateId()),
			Level:       storedLevel(e.GetLevel()),
			Template:    clipTo(e.GetTemplate(), maxTemplateBytes),
			Sample:      clipTo(e.GetSample(), maxSampleBytes),
			Count:       int(min(e.GetCount(), maxEventCount)),
			FirstTS:     clamp(e.GetFirstUnixMs()),
			LastTS:      clamp(e.GetLastUnixMs()),
		})
	}
	if err := g.store.InsertLogEvents(ctx, s.orgID, s.hostID, events); err != nil {
		g.log.Error("store log events", "host_id", s.hostID, "err", err)
	}

	cands := b.GetCandidates()
	if len(cands) > maxCandidatesPerBatch {
		cands = cands[:maxCandidatesPerBatch]
	}
	ts := make([]store.LogTemplate, 0, len(cands))
	for _, c := range cands {
		if c.GetTemplateId() == "" {
			continue
		}
		ts = append(ts, store.LogTemplate{
			TemplateID: clip(c.GetTemplateId()),
			Level:      storedLevel(c.GetLevel()),
			Template:   clipTo(c.GetTemplate(), maxTemplateBytes),
			Sample:     clipTo(c.GetSample(), maxSampleBytes),
		})
	}
	if err := g.store.UpsertTemplateCandidates(ctx, s.orgID, s.hostID, ts); err != nil {
		g.log.Error("store template candidates", "host_id", s.hostID, "err", err)
	}

	if st := b.GetStats(); st != nil {
		if err := g.store.SetLogStats(ctx, s.orgID, s.hostID, store.LogStats{
			LinesIn:           toInt64(st.GetLinesIn()),
			LinesSkipped:      toInt64(st.GetLinesSkipped()),
			EventsOut:         toInt64(st.GetEventsOut()),
			RedactionFailures: toInt64(st.GetRedactionFailures()),
			LinesDropped:      toInt64(st.GetLinesDropped()),
		}); err != nil {
			g.log.Error("store log stats", "host_id", s.hostID, "err", err)
		}
	}

	g.judgeAndPush(ctx, s)
}

// judgeAndPush judges the host's ready templates, at most every JudgeEvery,
// and sends the host's new green set to its own agent when something turned
// green. Other hosts are never affected.
func (g *Gateway) judgeAndPush(ctx context.Context, s *session) {
	g.judgeMu.Lock()
	ok := g.judge.allow(s.hostID, time.Now())
	g.judgeMu.Unlock()
	if !ok {
		return
	}
	changed, err := judgeHost(ctx, g.store, s.orgID, s.hostID)
	if err != nil {
		g.log.Error("judge templates", "host_id", s.hostID, "err", err)
		return
	}
	if changed {
		g.pushGreen(ctx, s)
	}
}
