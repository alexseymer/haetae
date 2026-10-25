package chat

import (
	"strings"
	"testing"
)

func TestEscapeHostileValues(t *testing.T) {
	hostile := "<b>bold</b> `code` \nnewline @channel </a> " + strings.Repeat("x", 10000)

	tg := EscapeTelegram(hostile)
	if strings.Contains(tg, "<b>") || strings.Contains(tg, "</a>") {
		t.Fatalf("telegram left tags: %q", tg)
	}
	if strings.Contains(tg, "\n") {
		t.Fatalf("telegram left newline: %q", tg)
	}

	sl := EscapeSlack(hostile)
	if strings.Contains(sl, "<b>") || strings.Contains(sl, "</a>") {
		t.Fatalf("slack left tags: %q", sl)
	}
	if strings.Contains(sl, "@channel") && strings.Contains(sl, "<") {
		t.Fatalf("slack unsafe: %q", sl)
	}

	plain := EscapePlain(hostile)
	if strings.Contains(plain, "\n") {
		t.Fatalf("plain left newline: %q", plain)
	}
	if len([]rune(plain)) > maxFieldRunes {
		t.Fatalf("plain too long: %d", len([]rune(plain)))
	}

	body := EscapeBody("telegram", hostile)
	if strings.Contains(body, "<b>") {
		t.Fatalf("body left tags: %q", body[:80])
	}
	if len([]rune(body)) > maxBodyRunes {
		t.Fatalf("body too long: %d", len([]rune(body)))
	}
}

func TestLimitRunes(t *testing.T) {
	if got := LimitRunes("abcd", 3); got != "ab…" {
		t.Fatalf("got %q", got)
	}
	if got := LimitRunes("ab", 3); got != "ab" {
		t.Fatalf("got %q", got)
	}
}

func TestEscapeSlackMentions(t *testing.T) {
	hostile := "ping <!channel> and <@U123> & more"
	got := EscapeSlack(hostile)
	if strings.Contains(got, "<!channel>") || strings.Contains(got, "<@U") {
		t.Fatalf("mention leaked: %q", got)
	}
	if !strings.Contains(got, "&amp;") || !strings.Contains(got, "&lt;") {
		t.Fatalf("expected entity escape: %q", got)
	}
}

func TestEscapeSlackMarkupSpecials(t *testing.T) {
	// Container-derived text must not introduce Slack mrkdwn emphasis.
	got := EscapeSlack("name *_~`evil*_~`")
	for _, ch := range []string{"*", "_", "~", "`"} {
		if strings.Contains(got, ch) && !strings.Contains(got, "\\"+ch) {
			t.Fatalf("unescaped %q in %q", ch, got)
		}
	}
	if !strings.Contains(got, "\\*") || !strings.Contains(got, "\\_") ||
		!strings.Contains(got, "\\~") || !strings.Contains(got, "\\`") {
		t.Fatalf("expected backslash escapes: %q", got)
	}
}
