package logpipe

import (
	"time"
)

const (
	defaultMergeWindow  = 10 * time.Second
	defaultMaxTemplates = 1000
	defaultMaxSampleLen = 512
	maxSeenIDs          = 4096
	maxNewTemplatesOut  = 64
)

// Config bounds the aggregator.
type Config struct {
	// MergeWindow is how long identical templates are merged; default 10s.
	MergeWindow time.Duration
	// MaxTemplates is the max distinct open templates held at once; default 1000.
	// When full, the least recently seen open entry is flushed early.
	MaxTemplates int
	// MaxSampleLen is bytes of the first raw line kept per entry; default 512.
	MaxSampleLen int
	// Now is an injectable clock; default time.Now.
	Now func() time.Time
	// Canon, if set, rewrites each template before its ID is computed and
	// before it is stored (the agent passes the redactor), so neither the
	// template nor its hash is derived from text that was not redacted. Results
	// are cached per raw template (at most canonCacheSize entries).
	Canon func(template string) string
	// SampleCanon, if set, rewrites the raw line BEFORE it is cut to
	// MaxSampleLen (the agent passes the redactor). Cutting first could leave a
	// prefix of a secret that straddles the cut, which no rule would recognise.
	SampleCanon func(line string) string
	// MaxPendingEvents bounds events waiting for the next Flush because of
	// MaxTemplates pressure; default 500. When exceeded, the least severe
	// event is dropped and counted in Stats.
	MaxPendingEvents int
}

const (
	canonCacheSize        = 256 // per aggregator; templates are at most 256 bytes
	defaultMaxPendingEvts = 500
)

// Event is one merged output record.
type Event struct {
	// TemplateID is the stable hash of level+template.
	TemplateID string
	// Level is the detected level of the merged lines.
	Level Level
	// Template is the shape string.
	Template string
	// Sample is the first raw line of the window, truncated to MaxSampleLen.
	Sample string
	// Count is the number of lines merged into this event.
	Count int
	// First is the time of the first line in the window.
	First time.Time
	// Last is the time of the most recent line in the window.
	Last time.Time
}

// Stats holds cumulative aggregator counters.
type Stats struct {
	// LinesIn is total lines offered to Add.
	LinesIn uint64
	// LinesSkipped is lines dropped by green-skipping.
	LinesSkipped uint64
	// EventsOut is events returned from Flush.
	EventsOut uint64
	// EarlyFlushes is forced flushes due to MaxTemplates.
	EarlyFlushes uint64
	// EventsDropped is events discarded because MaxPendingEvents was exceeded.
	EventsDropped uint64
	// LinesDropped is the number of lines inside those dropped events.
	LinesDropped uint64
}

// TemplateInfo describes a newly seen non-green template for server judgement.
type TemplateInfo struct {
	// ID is the TemplateID.
	ID string
	// Level is the detected level.
	Level Level
	// Template is the shape string.
	Template string
	// Sample is a truncated raw line sample.
	Sample string
}

type openEntry struct {
	id       string
	level    Level
	template string
	sample   string
	count    int
	first    time.Time
	last     time.Time
}

// Aggregator merges lines by template. Not safe for concurrent use.
type Aggregator struct {
	cfg        Config
	open       map[string]*openEntry // key = TemplateID
	order      []string              // LRU: least recently seen at index 0
	earlyBuf   []Event               // early-flushed events awaiting Flush
	green      *GreenSet
	canonCache map[string]string

	seen    map[string]struct{}
	seenOrd []string // oldest first; capped at maxSeenIDs
	pending []TemplateInfo

	stats Stats
}

// NewAggregator returns an Aggregator with defaults applied for zero fields.
func NewAggregator(cfg Config) *Aggregator {
	if cfg.MergeWindow <= 0 {
		cfg.MergeWindow = defaultMergeWindow
	}
	if cfg.MaxTemplates <= 0 {
		cfg.MaxTemplates = defaultMaxTemplates
	}
	if cfg.MaxSampleLen <= 0 {
		cfg.MaxSampleLen = defaultMaxSampleLen
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxPendingEvents <= 0 {
		cfg.MaxPendingEvents = defaultMaxPendingEvts
	}
	return &Aggregator{
		cfg:  cfg,
		open: make(map[string]*openEntry),
		seen: make(map[string]struct{}),
	}
}

// SetGreen replaces the set of template IDs the server has judged harmless.
// Safe to call between Add calls.
func (a *Aggregator) SetGreen(ids []string) {
	a.green = NewGreenSet(ids)
}

// SetGreenSet installs a set that may be shared with other aggregators, so
// many followed containers do not each hold their own copy.
func (a *Aggregator) SetGreenSet(g *GreenSet) {
	a.green = g
}

// Add records one line seen at time t. It returns whether the line was skipped
// by green-skipping (counted in stats only, never emitted).
func (a *Aggregator) Add(line string, t time.Time) (skipped bool) {
	a.stats.LinesIn++
	level := DetectLevel(line)
	tmpl := a.canon(Template(line))
	id := TemplateID(level, tmpl)

	if !level.AlwaysSend() {
		if a.green.Has(id) {
			a.stats.LinesSkipped++
			return true
		}
	}

	a.noteSeen(id, level, tmpl, line)

	if e, ok := a.open[id]; ok {
		e.count++
		e.last = t
		a.touch(id)
		return false
	}

	for len(a.open) >= a.cfg.MaxTemplates {
		a.earlyFlushOne()
	}

	sample := a.sample(line)
	a.open[id] = &openEntry{
		id:       id,
		level:    level,
		template: tmpl,
		sample:   sample,
		count:    1,
		first:    t,
		last:     t,
	}
	a.order = append(a.order, id)
	return false
}

// sample returns the evidence excerpt for a raw line: canonicalised first,
// then cut to size.
func (a *Aggregator) sample(line string) string {
	if a.cfg.SampleCanon != nil {
		line = a.cfg.SampleCanon(line)
	}
	return truncateRunes(line, a.cfg.MaxSampleLen)
}

// canon applies Config.Canon with a bounded cache. The cache is reset when it
// fills, which keeps memory fixed under high-cardinality input.
func (a *Aggregator) canon(raw string) string {
	if a.cfg.Canon == nil {
		return raw
	}
	if c, ok := a.canonCache[raw]; ok {
		return c
	}
	if a.canonCache == nil || len(a.canonCache) >= canonCacheSize {
		a.canonCache = make(map[string]string, 64)
	}
	c := a.cfg.Canon(raw)
	a.canonCache[raw] = c
	return c
}

// Flush returns events whose window ended (First+MergeWindow <= now) and
// removes them; with force=true returns all open events. Early-flushed
// events from MaxTemplates pressure are always included.
func (a *Aggregator) Flush(force bool) []Event {
	now := a.cfg.Now()
	out := a.earlyBuf
	a.earlyBuf = nil

	var keep []string
	for _, id := range a.order {
		e := a.open[id]
		if e == nil {
			continue
		}
		if force || !e.first.Add(a.cfg.MergeWindow).After(now) {
			out = append(out, Event{
				TemplateID: e.id,
				Level:      e.level,
				Template:   e.template,
				Sample:     e.sample,
				Count:      e.count,
				First:      e.first,
				Last:       e.last,
			})
			delete(a.open, id)
		} else {
			keep = append(keep, id)
		}
	}
	a.order = keep
	a.stats.EventsOut += uint64(len(out))
	return out
}

// Stats returns cumulative counters.
func (a *Aggregator) Stats() Stats {
	return a.stats
}

// NewTemplates returns IDs first seen since the last call and not green
// (candidates for the server to judge). At most 64 are returned. The seen-ID
// memory is bounded at 4096 entries (oldest evicted).
func (a *Aggregator) NewTemplates() []TemplateInfo {
	out := a.pending
	if len(out) > maxNewTemplatesOut {
		out = out[:maxNewTemplatesOut]
	}
	a.pending = nil
	return out
}

// openCount is used by tests to assert MaxTemplates bounding.
func (a *Aggregator) openCount() int {
	return len(a.open)
}

// seenCount is used by tests to assert seen-ID memory bounding.
func (a *Aggregator) seenCount() int {
	return len(a.seen)
}

func (a *Aggregator) noteSeen(id string, level Level, tmpl, line string) {
	if _, ok := a.seen[id]; ok {
		return
	}
	for len(a.seenOrd) >= maxSeenIDs {
		old := a.seenOrd[0]
		a.seenOrd = a.seenOrd[1:]
		delete(a.seen, old)
	}
	a.seen[id] = struct{}{}
	a.seenOrd = append(a.seenOrd, id)

	if a.green.Has(id) {
		return
	}
	if len(a.pending) >= maxNewTemplatesOut {
		return
	}
	a.pending = append(a.pending, TemplateInfo{
		ID:       id,
		Level:    level,
		Template: tmpl,
		Sample:   a.sample(line),
	})
}

func (a *Aggregator) touch(id string) {
	for i, x := range a.order {
		if x == id {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	a.order = append(a.order, id)
}

func (a *Aggregator) earlyFlushOne() {
	if len(a.order) == 0 {
		return
	}
	id := a.order[0]
	a.order = a.order[1:]
	e := a.open[id]
	delete(a.open, id)
	if e == nil {
		return
	}
	a.earlyBuf = append(a.earlyBuf, Event{
		TemplateID: e.id,
		Level:      e.level,
		Template:   e.template,
		Sample:     e.sample,
		Count:      e.count,
		First:      e.first,
		Last:       e.last,
	})
	a.stats.EarlyFlushes++
	for len(a.earlyBuf) > a.cfg.MaxPendingEvents {
		a.dropLeastSevere()
	}
}

// dropLeastSevere removes one pending event, preferring routine levels over
// warn, error, fatal and unknown, and older over newer.
func (a *Aggregator) dropLeastSevere() {
	victim := 0
	for i, ev := range a.earlyBuf {
		cur, best := a.earlyBuf[victim], ev
		if (!best.Level.AlwaysSend() && cur.Level.AlwaysSend()) ||
			(best.Level.AlwaysSend() == cur.Level.AlwaysSend() && best.Level < cur.Level) {
			victim = i
		}
	}
	a.stats.EventsDropped++
	a.stats.LinesDropped += uint64(a.earlyBuf[victim].Count) //nolint:gosec // G115: Count is positive
	a.earlyBuf = append(a.earlyBuf[:victim], a.earlyBuf[victim+1:]...)
}
