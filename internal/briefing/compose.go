// Package briefing builds the daily briefing text and schedules it once per
// local day. Compose is rule-based; the scheduler may hand its text to an
// optional AI Rewriter and falls back to it on any problem (S9).
package briefing

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alexseymer/haetae/internal/store"
)

const (
	maxLines       = 3
	maxChars       = 300
	maxNameRunes   = 48
	ellipsis       = "…"
	itemLinePrefix = "  "
)

// Compose turns facts into briefing body text: at most 3 lines and 300
// characters, Unicode-safe. A quiet day (no incident/alert items) is exactly
// one line. Hostile names are escaped to plain text and length-limited.
func Compose(facts store.BriefingFacts, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	hour := local.Format("15:04")

	total, running := 0, 0
	for state, n := range facts.Containers {
		total += n
		if state == "running" {
			running += n
		}
	}
	notRunning := total - running
	offline := facts.Hosts - facts.Online
	if offline < 0 {
		offline = 0
	}

	var status string
	switch {
	case offline == 0 && notRunning == 0:
		status = "all running."
	default:
		var parts []string
		if offline > 0 {
			parts = append(parts, fmt.Sprintf("%d offline", offline))
		}
		if notRunning > 0 {
			parts = append(parts, fmt.Sprintf("%d not running", notRunning))
		}
		status = strings.Join(parts, ", ") + "."
	}

	line1 := fmt.Sprintf("%s briefing, %d hosts, %d containers, %s",
		hour, facts.Hosts, total, status)

	items := rankItems(facts.Items)
	if len(items) == 0 {
		return truncateRunes(line1, maxChars)
	}

	lines := []string{line1}
	for i := 0; i < len(items) && len(lines) < maxLines; i++ {
		lines = append(lines, itemLinePrefix+formatItem(items[i]))
	}
	return fit(lines, maxLines, maxChars)
}

func rankItems(items []store.BriefingItem) []store.BriefingItem {
	out := append([]store.BriefingItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := store.SeverityRank(out[i].Severity), store.SeverityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		// Then recency (newer first). Prefer incidents over alerts when tied.
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "incident"
		}
		return out[i].Title < out[j].Title
	})
	return out
}

func formatItem(it store.BriefingItem) string {
	title := sanitizeName(it.Title, maxNameRunes*2)
	host := sanitizeName(it.HostName, maxNameRunes)
	if host == "" {
		return title
	}
	if title == "" {
		return host
	}
	if strings.Contains(strings.ToLower(title), strings.ToLower(host)) {
		return title
	}
	return title + " on " + host
}

// sanitizeName strips control characters and markup punctuation, collapses
// whitespace, and truncates on a rune boundary.
func sanitizeName(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\uFFFD':
			continue
		case unicode.IsControl(r) || r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case strings.ContainsRune("<>`*_[]()\\\"'", r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return truncateRunes(strings.Join(strings.Fields(b.String()), " "), max)
}

func fit(lines []string, maxLineCount, maxChars int) string {
	if len(lines) > maxLineCount {
		lines = lines[:maxLineCount]
	}
	body := strings.Join(lines, "\n")
	return truncateRunes(body, maxChars)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return ellipsis
	}
	var b strings.Builder
	b.Grow(max * utf8.UTFMax)
	n := 0
	for _, r := range s {
		if n >= max-1 {
			break
		}
		b.WriteRune(r)
		n++
	}
	b.WriteString(ellipsis)
	return b.String()
}
