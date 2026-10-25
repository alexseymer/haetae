package chat

import (
	"html"
	"strings"
	"unicode/utf8"
)

// Length limits for values rendered into chat messages.
const (
	maxTitleRunes   = 200
	maxSummaryRunes = 400
	maxBodyRunes    = 3500
	maxFieldRunes   = 120
)

// LimitRunes truncates s to at most n runes, appending an ellipsis when cut.
func LimitRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	if n == 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// EscapePlain removes control characters and length-limits for plain-text channels.
func EscapePlain(s string) string {
	s = stripControls(s)
	return LimitRunes(s, maxFieldRunes)
}

// EscapeTelegram escapes for Telegram HTML parse_mode.
func EscapeTelegram(s string) string {
	s = stripControls(s)
	s = LimitRunes(s, maxFieldRunes)
	return html.EscapeString(s)
}

// EscapeSlack escapes for Slack mrkdwn and neutralises mention sequences
// (<!channel>, <@U…>, <#C…>) so untrusted values cannot ping the channel.
func EscapeSlack(s string) string {
	s = stripControls(s)
	s = LimitRunes(s, maxFieldRunes)
	return escapeSlackMarkup(s)
}

func escapeSlackMarkup(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	s = replacer.Replace(s)
	// Break mention sequences after escaping so they cannot ping the channel.
	s = strings.ReplaceAll(s, "&lt;!", "&lt;\u200b!")
	s = strings.ReplaceAll(s, "&lt;@", "&lt;\u200b@")
	s = strings.ReplaceAll(s, "&lt;#", "&lt;\u200b#")
	return s
}

// EscapeBody applies channel-specific escaping and a body length limit.
func EscapeBody(channel, s string) string {
	s = stripControls(s)
	switch channel {
	case storeChannelTelegram:
		s = html.EscapeString(LimitRunes(s, maxBodyRunes))
	case storeChannelSlack:
		s = escapeSlackMarkup(LimitRunes(s, maxBodyRunes))
	default:
		s = LimitRunes(s, maxBodyRunes)
	}
	return LimitRunes(s, maxBodyRunes)
}

// store channel name constants mirrored locally to avoid a circular import in
// tests that only exercise escaping; values match store.Channel*.
const (
	storeChannelTelegram = "telegram"
	storeChannelSlack    = "slack"
)

func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
