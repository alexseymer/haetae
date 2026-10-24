// Package logpipe is the pure (no I/O) core of the agent's log pipeline:
// level detection, template extraction, merge-and-count aggregation, and a
// bounded ring buffer.
package logpipe

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Level is a normalised log severity.
type Level int

const (
	// LevelUnknown is returned when no level token is recognised.
	LevelUnknown Level = iota
	// LevelTrace is the most verbose level (pino 10, etc.).
	LevelTrace
	// LevelDebug is debug / fine.
	LevelDebug
	// LevelInfo is informational.
	LevelInfo
	// LevelWarn is warning / warn.
	LevelWarn
	// LevelError is error / err / severe.
	LevelError
	// LevelFatal is fatal / critical / panic / emerg / alert.
	LevelFatal
)

// String returns the canonical lowercase name of the level.
func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "trace"
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	case LevelFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// AlwaysSend reports whether lines of this level must never be dropped by
// green-skipping: warn, error, fatal, and unknown.
func (l Level) AlwaysSend() bool {
	switch l {
	case LevelWarn, LevelError, LevelFatal, LevelUnknown:
		return true
	default:
		return false
	}
}

// DetectLevel finds the level of one log line. It never panics on arbitrary bytes.
func DetectLevel(line string) Level {
	if line == "" {
		return LevelUnknown
	}

	// Special multi-line / stack-frame markers (checked first, exact prefixes).
	if specialError(line) {
		return LevelError
	}

	// Trim a leading syslog-like timestamp or whitespace for token scanning.
	s := strings.TrimLeftFunc(line, unicode.IsSpace)

	// JSON object line.
	if len(s) > 0 && s[0] == '{' {
		if lv, ok := detectJSONLevel(s); ok {
			return lv
		}
	}

	// glog: I/W/E/F followed by MMDD then space and time.
	if lv, ok := detectGlog(s); ok {
		return lv
	}

	// Bracketed level: [ERROR], [WARN], etc.
	if lv, ok := detectBracketLevel(s); ok {
		return lv
	}

	// logfmt / key=value level=...
	if lv, ok := detectLogfmtLevel(s); ok {
		return lv
	}

	// Python-style ERROR:root: or WARNING:pkg:
	if lv, ok := detectPythonLevel(s); ok {
		return lv
	}

	// Leading LEVEL: or LEVEL  (word boundary).
	if lv, ok := detectLeadingToken(s); ok {
		return lv
	}

	// Java SEVERE / WARNING / INFO / FINE as whole words anywhere early in the line.
	if lv, ok := detectJavaToken(s); ok {
		return lv
	}

	// Bare WARN / ERROR / INFO etc. as a token near the start (first ~80 bytes).
	if lv, ok := detectLooseToken(s); ok {
		return lv
	}

	return LevelUnknown
}

func specialError(line string) bool {
	switch {
	case strings.HasPrefix(line, "Traceback (most recent call last)"):
		return true
	case strings.HasPrefix(line, "panic:"):
		return true
	case strings.HasPrefix(line, "fatal error:"):
		return true
	case strings.HasPrefix(line, "Exception in thread"):
		return true
	case strings.HasPrefix(line, "\tat "):
		return true
	case strings.HasPrefix(line, "goroutine "):
		return true
	}
	return false
}

func detectGlog(s string) (Level, bool) {
	if len(s) < 6 {
		return 0, false
	}
	c := s[0]
	var lv Level
	switch c {
	case 'I':
		lv = LevelInfo
	case 'W':
		lv = LevelWarn
	case 'E':
		lv = LevelError
	case 'F':
		lv = LevelFatal
	default:
		return 0, false
	}
	// MMDD then space and digit (time).
	for i := 1; i <= 4; i++ {
		if i >= len(s) || s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	if len(s) < 6 || s[5] != ' ' {
		return 0, false
	}
	return lv, true
}

func detectBracketLevel(s string) (Level, bool) {
	i := strings.IndexByte(s, '[')
	if i < 0 || i > 40 {
		return 0, false
	}
	j := strings.IndexByte(s[i:], ']')
	if j < 2 || j > 16 {
		return 0, false
	}
	tok := s[i+1 : i+j]
	if lv, ok := parseLevelToken(tok); ok {
		return lv, true
	}
	return 0, false
}

func detectLogfmtLevel(s string) (Level, bool) {
	keys := []string{"level=", "lvl=", "severity=", "loglevel="}
	lower := asciiLower(s)
	for _, k := range keys {
		idx := strings.Index(lower, k)
		if idx < 0 {
			continue
		}
		rest := s[idx+len(k):]
		tok, ok := readLogfmtValue(rest)
		if !ok {
			continue
		}
		if lv, ok := parseLevelToken(tok); ok {
			return lv, true
		}
		if n, err := strconv.Atoi(tok); err == nil {
			if lv, ok := pinoLevel(n); ok {
				return lv, true
			}
		}
	}
	return 0, false
}

func readLogfmtValue(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if s[0] == '"' {
		end := 1
		for end < len(s) && s[end] != '"' {
			if s[end] == '\\' && end+1 < len(s) {
				end += 2
				continue
			}
			end++
		}
		if end >= len(s) {
			return s[1:], true
		}
		return s[1:end], true
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '}' {
			break
		}
		end++
	}
	if end == 0 {
		return "", false
	}
	return s[:end], true
}

func detectPythonLevel(s string) (Level, bool) {
	// ERROR:root: msg  or WARNING:package.module: msg
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || colon > 12 {
		return 0, false
	}
	tok := s[:colon]
	lv, ok := parseLevelToken(tok)
	if !ok {
		return 0, false
	}
	// Second colon shortly after (logger name).
	rest := s[colon+1:]
	colon2 := strings.IndexByte(rest, ':')
	if colon2 < 0 || colon2 > 64 {
		return 0, false
	}
	return lv, true
}

func detectLeadingToken(s string) (Level, bool) {
	// ERROR: ... or WARN ...
	end := 0
	for end < len(s) && end < 12 {
		c := s[end]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0, false
	}
	tok := s[:end]
	lv, ok := parseLevelToken(tok)
	if !ok {
		return 0, false
	}
	if end < len(s) {
		c := s[end]
		if c == ':' || c == ' ' || c == '\t' {
			return lv, true
		}
	}
	return 0, false
}

func detectJavaToken(s string) (Level, bool) {
	upper := asciiUpperPrefix(s, 120)
	for _, pair := range []struct {
		tok string
		lv  Level
	}{
		{"SEVERE", LevelError},
		{"WARNING", LevelWarn},
		{"FINEST", LevelTrace},
		{"FINER", LevelDebug},
		{"FINE", LevelDebug},
		{"INFO", LevelInfo},
	} {
		if idx := indexWord(upper, pair.tok); idx >= 0 {
			return pair.lv, true
		}
	}
	return 0, false
}

func detectLooseToken(s string) (Level, bool) {
	// Scan first ~100 bytes for whole-word level tokens.
	n := len(s)
	if n > 100 {
		n = 100
	}
	i := 0
	for i < n {
		// Skip non-letters.
		for i < n {
			c := s[i]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				break
			}
			i++
		}
		start := i
		for i < n {
			c := s[i]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				i++
				continue
			}
			break
		}
		if start < i {
			if lv, ok := parseLevelToken(s[start:i]); ok {
				return lv, true
			}
		}
		i++
	}
	return 0, false
}

func detectJSONLevel(s string) (Level, bool) {
	// Fast path: look for known keys without full JSON parse.
	keys := []string{
		`"level"`, `"lvl"`, `"severity"`, `"loglevel"`, `"log.level"`, `"@level"`,
	}
	lower := asciiLower(s)
	for _, k := range keys {
		kl := asciiLower(k)
		idx := 0
		for {
			j := strings.Index(lower[idx:], kl)
			if j < 0 {
				break
			}
			j += idx
			// Ensure key is a JSON key (preceded by { , whitespace).
			if j > 0 {
				prev := lower[j-1]
				if prev != '{' && prev != ',' && prev != ' ' && prev != '\t' && prev != '\n' {
					idx = j + 1
					continue
				}
			}
			rest := s[j+len(k):]
			rest = strings.TrimLeft(rest, " \t\n\r")
			if len(rest) == 0 || rest[0] != ':' {
				idx = j + 1
				continue
			}
			rest = strings.TrimLeft(rest[1:], " \t\n\r")
			if len(rest) == 0 {
				idx = j + 1
				continue
			}
			if rest[0] == '"' {
				end := 1
				for end < len(rest) && rest[end] != '"' {
					if rest[end] == '\\' && end+1 < len(rest) {
						end += 2
						continue
					}
					end++
				}
				tok := rest[1:end]
				if lv, ok := parseLevelToken(tok); ok {
					return lv, true
				}
			} else {
				// Numeric or bare.
				end := 0
				for end < len(rest) {
					c := rest[end]
					if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-' {
						end++
						continue
					}
					break
				}
				tok := rest[:end]
				if n, err := strconv.Atoi(tok); err == nil {
					if lv, ok := pinoLevel(n); ok {
						return lv, true
					}
				}
				if lv, ok := parseLevelToken(tok); ok {
					return lv, true
				}
			}
			idx = j + 1
		}
	}
	return 0, false
}

func pinoLevel(n int) (Level, bool) {
	switch n {
	case 10:
		return LevelTrace, true
	case 20:
		return LevelDebug, true
	case 30:
		return LevelInfo, true
	case 40:
		return LevelWarn, true
	case 50:
		return LevelError, true
	case 60:
		return LevelFatal, true
	default:
		return 0, false
	}
}

func parseLevelToken(tok string) (Level, bool) {
	if tok == "" {
		return 0, false
	}
	t := asciiLower(tok)
	switch t {
	case "trace", "trc":
		return LevelTrace, true
	case "debug", "dbg", "fine", "finer", "finest":
		return LevelDebug, true
	case "info", "information", "informational", "notice":
		return LevelInfo, true
	case "warn", "warning", "wrn":
		return LevelWarn, true
	case "error", "err", "severe", "erro":
		return LevelError, true
	case "fatal", "critical", "crit", "panic", "emerg", "emergency", "alert", "fatl":
		return LevelFatal, true
	}
	return 0, false
}

func asciiLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func asciiUpperPrefix(s string, n int) string {
	if n > len(s) {
		n = len(s)
	}
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func indexWord(s, word string) int {
	idx := 0
	for {
		j := strings.Index(s[idx:], word)
		if j < 0 {
			return -1
		}
		j += idx
		beforeOK := j == 0 || !isASCIILetterByte(s[j-1])
		after := j + len(word)
		afterOK := after >= len(s) || !isASCIILetterByte(s[after])
		if beforeOK && afterOK {
			return j
		}
		idx = j + 1
	}
}

func isASCIILetterByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// truncateRunes truncates s to at most maxBytes on a UTF-8 rune boundary.
func truncateRunes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// Walk back from maxBytes to a rune start.
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	if n == 0 {
		// First rune alone may exceed maxBytes; drop it.
		_, size := utf8.DecodeRuneInString(s)
		if size > maxBytes {
			return ""
		}
	}
	return s[:n]
}
