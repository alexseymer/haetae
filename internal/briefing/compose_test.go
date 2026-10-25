package briefing

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alexseymer/haetae/internal/store"
)

func TestComposeQuietDayOneLine(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, loc)
	facts := store.BriefingFacts{
		Hosts:  3,
		Online: 3,
		Containers: map[string]int{
			"running": 24,
		},
	}
	got := Compose(facts, now, loc)
	want := "08:00 briefing, 3 hosts, 24 containers, all running."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("quiet day must be exactly one line: %q", got)
	}
	if utf8.RuneCountInString(got) > maxChars {
		t.Fatalf("len %d > %d", utf8.RuneCountInString(got), maxChars)
	}
}

func TestComposeBusyDayLimits(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, loc)
	items := make([]store.BriefingItem, 0, 50)
	hostile := "<b>evil</b>\n`rm -rf` @channel </a> " + strings.Repeat("文", 200)
	for i := 0; i < 50; i++ {
		sev := store.SeverityLow
		switch i {
		case 0:
			sev = store.SeverityCritical
		case 1:
			sev = store.SeverityHigh
		}
		items = append(items, store.BriefingItem{
			Severity: sev,
			Title:    hostile + " title",
			HostName: hostile + "-host",
			Kind:     "incident",
		})
	}
	facts := store.BriefingFacts{
		Hosts:      2,
		Online:     1,
		Containers: map[string]int{"running": 10, "exited": 3},
		Items:      items,
	}
	got := Compose(facts, now, loc)
	lines := strings.Split(got, "\n")
	if len(lines) > maxLines {
		t.Fatalf("lines=%d body=%q", len(lines), got)
	}
	if utf8.RuneCountInString(got) > maxChars {
		t.Fatalf("runes=%d body=%q", utf8.RuneCountInString(got), got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, ">") || strings.Contains(got, "`") {
		t.Fatalf("markup leaked: %q", got)
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("cr leaked: %q", got)
	}
	// Interior newlines only as line separators (at most 2).
	if strings.Count(got, "\n") > maxLines-1 {
		t.Fatalf("too many newlines: %q", got)
	}
	if !strings.HasPrefix(lines[0], "08:00 briefing, 2 hosts, 13 containers,") {
		t.Fatalf("line1=%q", lines[0])
	}
	if !strings.Contains(lines[0], "1 offline") || !strings.Contains(lines[0], "3 not running") {
		t.Fatalf("expected problem counts in %q", lines[0])
	}
}

func TestComposeTruncatesOnRuneBoundary(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, loc)
	long := strings.Repeat("世", 400)
	facts := store.BriefingFacts{
		Hosts:  1,
		Online: 1,
		Containers: map[string]int{
			"running": 1,
		},
		Items: []store.BriefingItem{
			{Severity: store.SeverityCritical, Title: long, HostName: "h", Kind: "incident"},
			{Severity: store.SeverityHigh, Title: strings.Repeat("界", 400), HostName: "h2", Kind: "incident"},
		},
	}
	got := Compose(facts, now, loc)
	if utf8.RuneCountInString(got) > maxChars {
		t.Fatalf("runes=%d", utf8.RuneCountInString(got))
	}
	if !strings.Contains(got, ellipsis) {
		t.Fatalf("expected ellipsis: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid utf8")
	}
}

func TestTruncateRunes(t *testing.T) {
	s := strings.Repeat("世", 10)
	got := truncateRunes(s, 5)
	if utf8.RuneCountInString(got) != 5 || !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("got %q (runes=%d)", got, utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid utf8")
	}
	// Body hard-cap: three long lines joined then truncated.
	lines := []string{
		strings.Repeat("a", 120),
		strings.Repeat("b", 120),
		strings.Repeat("c", 120),
	}
	body := fit(lines, maxLines, maxChars)
	if utf8.RuneCountInString(body) > maxChars || !strings.HasSuffix(body, ellipsis) {
		t.Fatalf("fit=%q runes=%d", body, utf8.RuneCountInString(body))
	}
}
