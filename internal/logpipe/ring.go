package logpipe

import (
	"sync"
	"time"
)

// Entry is one line stored in a Ring.
type Entry struct {
	// T is the observation time.
	T time.Time
	// Line is the stored (possibly truncated) text.
	Line string
}

// Ring keeps the most recent lines for on-demand queries, bounded by both
// line count and total bytes. Safe for concurrent use.
type Ring struct {
	mu       sync.Mutex
	maxLines int
	maxBytes int
	entries  []Entry
	bytes    int
}

// NewRing creates a Ring bounded by maxLines and maxBytes. Non-positive
// bounds are treated as zero (nothing stored).
func NewRing(maxLines, maxBytes int) *Ring {
	return &Ring{
		maxLines: maxLines,
		maxBytes: maxBytes,
	}
}

// Add appends a line, truncating it if longer than maxBytes, then evicts
// oldest entries until both bounds hold.
func (r *Ring) Add(t time.Time, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.maxLines <= 0 || r.maxBytes <= 0 {
		return
	}

	if len(line) > r.maxBytes {
		line = truncateRunes(line, r.maxBytes)
	}

	r.entries = append(r.entries, Entry{T: t, Line: line})
	r.bytes += len(line)

	for len(r.entries) > r.maxLines || r.bytes > r.maxBytes {
		old := r.entries[0]
		r.entries = r.entries[1:]
		r.bytes -= len(old.Line)
	}
}

// Query returns entries with from <= T <= to, oldest first, at most max.
// A non-positive max means no limit beyond the ring contents.
func (r *Ring) Query(from, to time.Time, max int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Entry
	for _, e := range r.entries {
		if e.T.Before(from) || e.T.After(to) {
			continue
		}
		out = append(out, e)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

// Len returns the number of stored lines.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Bytes returns the total stored byte count of line bodies.
func (r *Ring) Bytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}
