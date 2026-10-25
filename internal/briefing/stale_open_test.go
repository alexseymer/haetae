package briefing

import (
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

func TestComposeIncludesStaleOpenIncidentsOrderedBySeverityThenRecency(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, loc)
	old := now.Add(-72 * time.Hour) // older than 24h window
	newer := now.Add(-48 * time.Hour)
	facts := store.BriefingFacts{
		Hosts:      2,
		Online:     2,
		Containers: map[string]int{"running": 1},
		Items: []store.BriefingItem{
			{Severity: store.SeverityMedium, Title: "old-medium", HostName: "h1", Kind: "incident", At: old},
			{Severity: store.SeverityHigh, Title: "older-high", HostName: "h2", Kind: "incident", At: old},
			{Severity: store.SeverityHigh, Title: "newer-high", HostName: "h3", Kind: "incident", At: newer},
			{Severity: store.SeverityCritical, Title: "stale-critical", HostName: "h4", Kind: "incident", At: old},
		},
	}
	got := Compose(facts, now, loc)
	lines := strings.Split(got, "\n")
	if len(lines) > maxLines {
		t.Fatalf("lines=%d body=%q", len(lines), got)
	}
	// Stale open critical must appear (intentional: unresolved always relevant).
	if !strings.Contains(got, "stale-critical") {
		t.Fatalf("stale open critical missing: %q", got)
	}
	// Cap at 2 item lines; severity then recency → critical, then newer-high.
	if len(lines) < 3 {
		t.Fatalf("want header + 2 items, got %q", got)
	}
	if !strings.Contains(lines[1], "stale-critical") {
		t.Fatalf("line1 item should be critical: %q", lines[1])
	}
	if !strings.Contains(lines[2], "newer-high") {
		t.Fatalf("line2 item should be newer high (recency): %q", lines[2])
	}
	if strings.Contains(got, "older-high") || strings.Contains(got, "old-medium") {
		t.Fatalf("lower-ranked items should be capped out: %q", got)
	}
}
