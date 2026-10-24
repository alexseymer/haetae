package agent

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/logpipe"
	"github.com/alexseymer/haetae/internal/redact"
)

// LogConfig tunes log collection. Zero values get defaults.
type LogConfig struct {
	FlushEvery      time.Duration // how often merged events are sent, default 2 s
	RedactionBudget time.Duration // per batch, default 500 ms
	MaxContainers   int           // followed at once, default 64
	RingLines       int           // per container, default 1000
	RingBytes       int           // per container, default 128 KiB
	MergeWindow     time.Duration // default 10 s
}

func (c LogConfig) withDefaults() LogConfig {
	if c.FlushEvery <= 0 {
		c.FlushEvery = 2 * time.Second
	}
	if c.RedactionBudget <= 0 {
		c.RedactionBudget = 500 * time.Millisecond
	}
	if c.MaxContainers <= 0 {
		c.MaxContainers = 64
	}
	if c.RingLines <= 0 {
		c.RingLines = 1000
	}
	if c.RingBytes <= 0 {
		c.RingBytes = 128 << 10
	}
	if c.MergeWindow <= 0 {
		c.MergeWindow = 10 * time.Second
	}
	return c
}

// maxQueryLines caps one on-demand log query.
const maxQueryLines = 1000

// LogManager follows the logs of running containers, merges and filters them,
// and sends the result through the Outbox. Raw lines stay on the host: only
// merged, redacted events leave it, and only when redaction succeeded.
type LogManager struct {
	cfg    LogConfig
	docker *dockerapi.Client
	out    *Outbox
	redact *redact.Redactor
	log    *slog.Logger

	green atomic.Pointer[logpipe.GreenSet]

	mu         sync.Mutex
	followers  map[string]*follower // by container id
	redactFail atomic.Uint64
	dropped    atomic.Uint64
}

type follower struct {
	cancel context.CancelFunc
	name   string

	mu   sync.Mutex // guards agg, which Add and Flush share
	agg  *logpipe.Aggregator
	ring *logpipe.Ring
}

// NewLogManager returns a manager writing to out.
func NewLogManager(cfg LogConfig, docker *dockerapi.Client, out *Outbox, log *slog.Logger) *LogManager {
	cfg = cfg.withDefaults()
	return &LogManager{
		cfg:       cfg,
		docker:    docker,
		out:       out,
		redact:    redact.New(),
		log:       log,
		followers: make(map[string]*follower),
	}
}

// SetGreen installs the server's green set.
func (m *LogManager) SetGreen(ids []string) {
	m.green.Store(logpipe.NewGreenSet(ids))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.followers {
		f.mu.Lock()
		f.agg.SetGreenSet(m.green.Load())
		f.mu.Unlock()
	}
}

// Sync starts followers for newly running containers and stops those that
// are gone. running maps container id to name. The cap bounds memory; extra
// containers are not followed and counted as dropped streams.
func (m *LogManager) Sync(ctx context.Context, running map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, f := range m.followers {
		if _, ok := running[id]; !ok {
			f.cancel()
			delete(m.followers, id)
		}
	}
	for id, name := range running {
		if _, ok := m.followers[id]; ok {
			continue
		}
		if len(m.followers) >= m.cfg.MaxContainers {
			m.log.Warn("log follower limit reached; container not followed", "container", name)
			continue
		}
		m.startFollower(ctx, id, name)
	}
}

func (m *LogManager) startFollower(ctx context.Context, id, name string) {
	fctx, cancel := context.WithCancel(ctx)
	f := &follower{
		cancel: cancel,
		name:   name,
		agg: logpipe.NewAggregator(logpipe.Config{
			MergeWindow:  m.cfg.MergeWindow,
			MaxTemplates: 200,
			MaxSampleLen: 256,
		}),
		ring: logpipe.NewRing(m.cfg.RingLines, m.cfg.RingBytes),
	}
	f.agg.SetGreenSet(m.green.Load())
	m.followers[id] = f
	go m.follow(fctx, id, f)
}

func (m *LogManager) follow(ctx context.Context, id string, f *follower) {
	since := time.Now()
	backoff := time.Second
	for ctx.Err() == nil {
		detail, err := m.docker.InspectContainer(ctx, id)
		if err == nil {
			started := time.Now()
			err = m.docker.Logs(ctx, id, detail.Config.Tty, since, func(l dockerapi.LogLine) {
				t := l.Time
				if t.IsZero() {
					t = time.Now()
				}
				since = t
				f.ring.Add(t, l.Text)
				f.mu.Lock()
				f.agg.Add(l.Text, t)
				f.mu.Unlock()
			})
			if time.Since(started) > time.Minute {
				backoff = time.Second
			}
		}
		if ctx.Err() != nil {
			return
		}
		m.log.Debug("log stream ended", "container", f.name, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// Run flushes merged events every FlushEvery until ctx ends.
func (m *LogManager) Run(ctx context.Context) {
	t := time.NewTicker(m.cfg.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.flush(ctx)
		}
	}
}

func levelToProto(l logpipe.Level) agentv1.LogLevel { return agentv1.LogLevel(l) } //nolint:gosec // G115: Level is 0..6

func (m *LogManager) flush(ctx context.Context) {
	type item struct {
		container string
		ev        logpipe.Event
	}
	var items []item
	var cands []logpipe.TemplateInfo
	var stats logpipe.Stats

	m.mu.Lock()
	for id, f := range m.followers {
		f.mu.Lock()
		for _, ev := range f.agg.Flush(false) {
			items = append(items, item{id, ev})
		}
		cands = append(cands, f.agg.NewTemplates()...)
		st := f.agg.Stats()
		f.mu.Unlock()
		stats.LinesIn += st.LinesIn
		stats.LinesSkipped += st.LinesSkipped
		stats.EventsOut += st.EventsOut
	}
	m.mu.Unlock()
	if len(items) == 0 && len(cands) == 0 {
		return
	}

	// Redact everything that will leave the host in one batch: for each event
	// the template and the sample, for each candidate the same. If redaction
	// fails for any reason, nothing from this batch is sent.
	texts := make([]string, 0, 2*(len(items)+len(cands)))
	for _, it := range items {
		texts = append(texts, it.ev.Template, it.ev.Sample)
	}
	for _, c := range cands {
		texts = append(texts, c.Template, c.Sample)
	}
	res, err := m.redact.RedactBatch(ctx, texts, m.cfg.RedactionBudget)
	if err != nil {
		m.redactFail.Add(1)
		m.log.Warn("redaction failed; log batch not sent", "err", err, "events", len(items))
		return
	}

	batch := &agentv1.LogBatch{}
	for i, it := range items {
		batch.Events = append(batch.Events, &agentv1.LogEvent{
			ContainerId: it.container,
			TemplateId:  it.ev.TemplateID,
			Level:       levelToProto(it.ev.Level),
			Template:    res.Lines[2*i],
			Sample:      res.Lines[2*i+1],
			Count:       uint32(min(it.ev.Count, 1<<31)), //nolint:gosec // G115: clamped
			FirstUnixMs: it.ev.First.UnixMilli(),
			LastUnixMs:  it.ev.Last.UnixMilli(),
		})
	}
	off := 2 * len(items)
	for i, c := range cands {
		batch.Candidates = append(batch.Candidates, &agentv1.TemplateCandidate{
			TemplateId: c.ID,
			Level:      levelToProto(c.Level),
			Template:   res.Lines[off+2*i],
			Sample:     res.Lines[off+2*i+1],
		})
	}
	// Fill everything before queueing: once queued, the forwarder owns the batch.
	batch.Stats = m.statsProto(stats)
	m.dropped.Add(uint64(m.out.PutLogs(batch))) //nolint:gosec // G115: non-negative count
}

func (m *LogManager) statsProto(s logpipe.Stats) *agentv1.LogStats {
	return &agentv1.LogStats{
		LinesIn:           s.LinesIn,
		LinesSkipped:      s.LinesSkipped,
		EventsOut:         s.EventsOut,
		RedactionFailures: m.redactFail.Load(),
		LinesDropped:      m.dropped.Load(),
	}
}

// QueryLogs answers an on-demand request from the ring buffer. Lines are
// redacted before they are returned; if redaction fails the answer carries an
// error and no lines.
func (m *LogManager) QueryLogs(ctx context.Context, q *agentv1.QueryLogs) *agentv1.CommandResult {
	m.mu.Lock()
	f := m.followers[q.GetContainerId()]
	m.mu.Unlock()
	if f == nil {
		return &agentv1.CommandResult{Error: "container is not being followed"}
	}
	limit := int(min(q.GetMaxLines(), maxQueryLines))
	if limit == 0 {
		limit = 200
	}
	from, to := time.UnixMilli(q.GetFromUnixMs()), time.UnixMilli(q.GetToUnixMs())
	if q.GetToUnixMs() == 0 {
		to = time.Now()
	}
	entries := f.ring.Query(from, to, limit+1)
	truncated := len(entries) > limit
	entries = entries[:min(len(entries), limit)]

	texts := make([]string, len(entries))
	for i, e := range entries {
		texts[i] = e.Line
	}
	res, err := m.redact.RedactBatch(ctx, texts, m.cfg.RedactionBudget)
	if err != nil {
		m.redactFail.Add(1)
		return &agentv1.CommandResult{Error: "redaction failed; no lines returned"}
	}
	out := &agentv1.LogLines{Truncated: truncated}
	for i, e := range entries {
		out.Lines = append(out.Lines, &agentv1.LogLine{TsUnixMs: e.T.UnixMilli(), Line: res.Lines[i]})
	}
	return &agentv1.CommandResult{Payload: &agentv1.CommandResult_LogLines{LogLines: out}}
}

// Handle runs the log-related commands. It returns nil for other kinds.
func (m *LogManager) Handle(ctx context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
	if q := cmd.GetQueryLogs(); q != nil {
		return m.QueryLogs(ctx, q)
	}
	return nil
}
