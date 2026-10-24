package logpipe

import (
	"hash/fnv"
	"strings"
	"unicode/utf8"
)

const templateMaxBytes = 256

// Template reduces a line to its shape so repeats can be merged: replaces
// numbers, hex strings >= 8 chars, UUIDs, IPv4/IPv6 addresses, quoted strings,
// ISO/RFC3339 timestamps and syslog timestamps, durations, sizes, file paths
// with >= 2 separators, and long base64-like tokens with placeholders.
// Runs of whitespace are collapsed. Output is capped at 256 bytes.
func Template(line string) string {
	if line == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(line))
	i := 0
	n := len(line)
	lastWasSpace := false
	emit := func(s string) {
		if s == " " {
			if lastWasSpace || b.Len() == 0 {
				return
			}
			lastWasSpace = true
			b.WriteByte(' ')
			return
		}
		lastWasSpace = false
		b.WriteString(s)
	}
	for i < n {
		c := line[i]

		// Whitespace collapse.
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			emit(" ")
			i++
			continue
		}

		// Quoted string.
		if c == '"' || c == '\'' {
			q := c
			j := i + 1
			for j < n {
				if line[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				if line[j] == q {
					j++
					break
				}
				j++
			}
			emit("<STR>")
			i = j
			continue
		}

		// Try multi-char patterns at current position.
		if tok, adv := matchPlaceholder(line, i); adv > 0 {
			emit(tok)
			i += adv
			continue
		}

		// Single rune (UTF-8 safe).
		r, size := utf8.DecodeRuneInString(line[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte: keep as-is.
			emit(string(c))
			i++
			continue
		}
		emit(string(r))
		i += size
	}
	out := strings.TrimSpace(b.String())
	return truncateRunes(out, templateMaxBytes)
}

// TemplateID is a stable 64-bit FNV-1a hash of level.String()+"\x00"+template,
// formatted as 16 lowercase hex chars.
func TemplateID(level Level, template string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(level.String()))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(template))
	sum := h.Sum64()
	const hexdigits = "0123456789abcdef"
	var buf [16]byte
	for i := 15; i >= 0; i-- {
		buf[i] = hexdigits[sum&0xf]
		sum >>= 4
	}
	return string(buf[:])
}

// matchPlaceholder tries to match a replaceable token at i. Returns placeholder and byte advance.
func matchPlaceholder(s string, i int) (string, int) {
	n := len(s)

	// ISO / RFC3339-ish: 2024-01-15T12:34:56 or 2024-01-15 12:34:56 with optional frac/Z/offset
	if adv := matchTimestamp(s, i); adv > 0 {
		return "<TS>", adv
	}

	// Syslog timestamp: "Jan  2 15:04:05" or "Jan 02 15:04:05"
	if adv := matchSyslogTS(s, i); adv > 0 {
		return "<TS>", adv
	}

	// UUID: 8-4-4-4-12 hex
	if adv := matchUUID(s, i); adv > 0 {
		return "<UUID>", adv
	}

	// IPv4 or IPv6
	if adv := matchIPv4(s, i); adv > 0 {
		return "<IP>", adv
	}
	if adv := matchIPv6(s, i); adv > 0 {
		return "<IP>", adv
	}

	// Duration: 12ms, 1.5s, 100us, 2m, 3h
	if adv := matchDuration(s, i); adv > 0 {
		return "<DUR>", adv
	}

	// Size: 10KB, 1.5MB, 100B, 2GiB
	if adv := matchSize(s, i); adv > 0 {
		return "<SIZE>", adv
	}

	// File path with >= 2 separators
	if adv := matchPath(s, i); adv > 0 {
		return "<PATH>", adv
	}

	// Hex string >= 8 chars
	if adv := matchHex(s, i); adv >= 8 {
		return "<HEX>", adv
	}

	// Long base64-like token (>= 20 chars of base64 alphabet)
	if adv := matchBase64Token(s, i); adv >= 20 {
		return "<TOK>", adv
	}

	// Number (int or float)
	if adv := matchNumber(s, i); adv > 0 {
		return "<NUM>", adv
	}

	_ = n
	return "", 0
}

func matchTimestamp(s string, i int) int {
	// YYYY-MM-DD[ T]HH:MM:SS[.frac][Z|±HH:MM]
	if i+10 > len(s) {
		return 0
	}
	if !isDigits(s[i:i+4]) || s[i+4] != '-' || !isDigits(s[i+5:i+7]) || s[i+7] != '-' || !isDigits(s[i+8:i+10]) {
		return 0
	}
	j := i + 10
	if j >= len(s) || (s[j] != 'T' && s[j] != ' ' && s[j] != 't') {
		return 10
	}
	sep := j
	j++
	if j+8 > len(s) || !isDigits(s[j:j+2]) || s[j+2] != ':' || !isDigits(s[j+3:j+5]) || s[j+5] != ':' || !isDigits(s[j+6:j+8]) {
		return sep - i // date only
	}
	j += 8
	if j < len(s) && s[j] == '.' {
		j++
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
	}
	if j < len(s) && (s[j] == 'Z' || s[j] == 'z') {
		j++
	} else if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
		digits := 0
		for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ':') {
			if s[j] != ':' {
				digits++
			}
			j++
			if digits >= 4 {
				break
			}
		}
	}
	return j - i
}

func matchSyslogTS(s string, i int) int {
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	if i+15 > len(s) {
		return 0
	}
	mon := s[i : i+3]
	ok := false
	for _, m := range months {
		if mon == m {
			ok = true
			break
		}
	}
	if !ok {
		return 0
	}
	if s[i+3] != ' ' {
		return 0
	}
	j := i + 4
	// day: " 2" or "02" or "12"
	if j < len(s) && s[j] == ' ' {
		j++
	}
	if j >= len(s) || s[j] < '0' || s[j] > '9' {
		return 0
	}
	j++
	if j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j >= len(s) || s[j] != ' ' {
		return 0
	}
	j++
	// HH:MM:SS
	if j+8 > len(s) || !isDigits(s[j:j+2]) || s[j+2] != ':' || !isDigits(s[j+3:j+5]) || s[j+5] != ':' || !isDigits(s[j+6:j+8]) {
		return 0
	}
	return j + 8 - i
}

func matchUUID(s string, i int) int {
	// 8-4-4-4-12
	need := 36
	if i+need > len(s) {
		return 0
	}
	parts := []int{8, 4, 4, 4, 12}
	j := i
	for pi, plen := range parts {
		if pi > 0 {
			if s[j] != '-' {
				return 0
			}
			j++
		}
		for k := 0; k < plen; k++ {
			if !isHexByte(s[j]) {
				return 0
			}
			j++
		}
	}
	// Don't absorb trailing hex that would make it not a UUID boundary.
	if j < len(s) && isHexByte(s[j]) {
		return 0
	}
	return j - i
}

func matchIPv4(s string, i int) int {
	j := i
	dots := 0
	for seg := 0; seg < 4; seg++ {
		if j >= len(s) || s[j] < '0' || s[j] > '9' {
			return 0
		}
		start := j
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j-start > 3 {
			return 0
		}
		// value 0-255 loosely (length check enough for shape)
		if seg < 3 {
			if j >= len(s) || s[j] != '.' {
				return 0
			}
			j++
			dots++
		}
	}
	if dots != 3 {
		return 0
	}
	return j - i
}

func matchIPv6(s string, i int) int {
	// Require at least two colons and mostly hex/colon.
	if i >= len(s) || (!isHexByte(s[i]) && s[i] != ':') {
		return 0
	}
	j := i
	colons := 0
	hexes := 0
	for j < len(s) {
		c := s[j]
		if isHexByte(c) {
			hexes++
			j++
			continue
		}
		if c == ':' {
			colons++
			j++
			continue
		}
		break
	}
	if colons < 2 || hexes < 2 {
		return 0
	}
	// Must not be just "::" alone without looking like address — allow "::1" style.
	if j-i < 4 {
		return 0
	}
	return j - i
}

func matchDuration(s string, i int) int {
	j := i
	if j >= len(s) || (s[j] != '.' && (s[j] < '0' || s[j] > '9')) {
		return 0
	}
	sawDigit := false
	sawDot := false
	for j < len(s) {
		c := s[j]
		if c >= '0' && c <= '9' {
			sawDigit = true
			j++
			continue
		}
		if c == '.' && !sawDot {
			sawDot = true
			j++
			continue
		}
		break
	}
	if !sawDigit {
		return 0
	}
	rest := s[j:]
	units := []string{"ms", "us", "µs", "ns", "s", "m", "h"}
	for _, u := range units {
		if strings.HasPrefix(rest, u) {
			// Avoid matching bare 'm'/'h'/'s' as part of a word.
			end := j + len(u)
			if end < len(s) && isASCIILetterByte(s[end]) {
				continue
			}
			return end - i
		}
	}
	return 0
}

func matchSize(s string, i int) int {
	j := i
	if j >= len(s) || s[j] < '0' || s[j] > '9' {
		return 0
	}
	sawDot := false
	for j < len(s) {
		c := s[j]
		if c >= '0' && c <= '9' {
			j++
			continue
		}
		if c == '.' && !sawDot {
			sawDot = true
			j++
			continue
		}
		break
	}
	rest := s[j:]
	units := []string{"KiB", "MiB", "GiB", "TiB", "KB", "MB", "GB", "TB", "kB", "B"}
	for _, u := range units {
		if strings.HasPrefix(rest, u) {
			end := j + len(u)
			if end < len(s) && isASCIILetterByte(s[end]) {
				continue
			}
			return end - i
		}
	}
	return 0
}

func matchPath(s string, i int) int {
	c := s[i]
	// Absolute unix path or windows-ish
	if c != '/' && c != '\\' {
		// relative path starting with ./ or ../
		if c != '.' || i+1 >= len(s) || (s[i+1] != '/' && s[i+1] != '\\' && (s[i+1] != '.' || i+2 >= len(s) || (s[i+2] != '/' && s[i+2] != '\\'))) {
			return 0
		}
	}
	j := i
	seps := 0
	for j < len(s) {
		ch := s[j]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '"' || ch == '\'' || ch == ',' || ch == ';' || ch == ')' || ch == ']' || ch == '}' {
			break
		}
		if ch == '/' || ch == '\\' {
			seps++
		}
		// stop on clearly non-path if we've started
		j++
	}
	if seps < 2 {
		return 0
	}
	return j - i
}

func matchHex(s string, i int) int {
	j := i
	for j < len(s) && isHexByte(s[j]) {
		j++
	}
	n := j - i
	if n < 8 {
		return 0
	}
	// Prefer not to steal pure decimal numbers: if all digits and no a-f, let number matcher handle short ones;
	// but >= 8 all-digit is still fine as <HEX> or <NUM>. Prefer <NUM> for pure decimals.
	allDigit := true
	for k := i; k < j; k++ {
		if s[k] < '0' || s[k] > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return 0
	}
	return n
}

func matchBase64Token(s string, i int) int {
	j := i
	for j < len(s) {
		c := s[j]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			j++
			continue
		}
		break
	}
	n := j - i
	if n < 20 {
		return 0
	}
	// Must look token-like: mixed or has +/ or long.
	hasSpecial := false
	hasLetter := false
	for k := i; k < j; k++ {
		c := s[k]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			hasLetter = true
		}
		if c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			hasSpecial = true
		}
	}
	if !hasLetter {
		return 0
	}
	// Avoid eating normal words: require special base64 char OR length >= 32.
	if !hasSpecial && n < 32 {
		return 0
	}
	return n
}

func matchNumber(s string, i int) int {
	j := i
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		// Only if followed by digit.
		if j+1 >= len(s) || s[j+1] < '0' || s[j+1] > '9' {
			return 0
		}
		j++
	}
	start := j
	if j >= len(s) || s[j] < '0' || s[j] > '9' {
		return 0
	}
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j < len(s) && s[j] == '.' {
		j++
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
	}
	if j == start {
		return 0
	}
	return j - i
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
