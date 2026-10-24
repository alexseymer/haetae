package redact

import (
	"regexp"
	"strings"
	"unicode"
)

// rule is one precompiled redaction pattern.
type rule struct {
	name string
	kind Kind
	// apply redacts matches in s and returns the new string plus replacement count.
	// Must not retain s. May panic (recovered by the caller).
	apply func(s string) (string, int)
}

func defaultRules() []rule {
	secrets := []rule{
		reRule("aws-access-key", KindSecret, `(?:AKIA|ASIA)[A-Z0-9]{16}`),
		awsSecretKeyRule(),
		privateKeyRule(),
		reRule("github-token", KindSecret, `gh[pousr]_[A-Za-z0-9]{36,}`),
		reRule("slack-token", KindSecret, `xox[baprs]-[A-Za-z0-9-]{10,}`),
		reRule("stripe-key", KindSecret, `(?:sk_live_|rk_live_)[A-Za-z0-9]{20,}`),
		reRule("google-api-key", KindSecret, `AIza[0-9A-Za-z_\-]{35}`),
		reRule("jwt", KindSecret, `eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
		reRule("bearer-token", KindSecret, `(?i)Bearer\s+[A-Za-z0-9\-._~+/]{16,}=*`),
		reRule("basic-auth-header", KindSecret, `(?i)Authorization:\s*Basic\s+[A-Za-z0-9+/=]+`),
		urlCredentialsRule(),
		genericSecretRule(),
	}
	pii := []rule{
		creditCardRule(),
		reRule("email", KindPII, `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
		ipv6Rule(),
		ipv4Rule(),
		phoneRule(),
	}
	out := make([]rule, 0, len(secrets)+len(pii))
	out = append(out, secrets...)
	out = append(out, pii...)
	return out
}

func reRule(name string, kind Kind, pattern string) rule {
	re := regexp.MustCompile(pattern)
	token := "[REDACTED:" + name + "]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: name,
		kind: kind,
		apply: func(s string) (string, int) {
			n := 0
			out := re.ReplaceAllStringFunc(s, func(string) string {
				n++
				return token
			})
			return out, n
		},
	}
}

func awsSecretKeyRule() rule {
	re := regexp.MustCompile(`(?i)(aws_secret_access_key)(\s*[=:]\s*)([A-Za-z0-9/+=]{40})`)
	token := "[REDACTED:aws-secret-key]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "aws-secret-key",
		kind: KindSecret,
		apply: func(s string) (string, int) {
			n := 0
			out := re.ReplaceAllStringFunc(s, func(m string) string {
				sub := re.FindStringSubmatch(m)
				if len(sub) < 4 {
					return m
				}
				n++
				return sub[1] + sub[2] + token
			})
			return out, n
		},
	}
}

func privateKeyRule() rule {
	// The BEGIN header and everything that looks like key material after it,
	// including spaces (single-line logging often joins PEM lines with spaces),
	// up to and including an END trailer. A key spread over several log lines
	// is handled by PEMGuard, which blanks the continuation lines.
	re := regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\sA-Za-z0-9+/=:,.\-]*`)
	token := "[REDACTED:private-key]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "private-key",
		kind: KindSecret,
		apply: func(s string) (string, int) {
			n := 0
			out := re.ReplaceAllStringFunc(s, func(string) string {
				n++
				return token
			})
			return out, n
		},
	}
}

func urlCredentialsRule() rule {
	// Any scheme: database and broker URLs (postgres://, redis://, amqp://) carry passwords too.
	re := regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^:\s/@]+):([^@\s/]+)(@)`)
	token := "[REDACTED:url-credentials]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "url-credentials",
		kind: KindSecret,
		apply: func(s string) (string, int) {
			n := 0
			out := re.ReplaceAllStringFunc(s, func(m string) string {
				n++
				sub := re.FindStringSubmatch(m)
				if len(sub) < 5 {
					return token
				}
				return sub[1] + token + sub[4]
			})
			return out, n
		},
	}
}

// alreadyRedacted reports whether a matched value is an earlier rule's
// placeholder, which must not be replaced again (it would lose the more
// specific rule name).
func alreadyRedacted(v string) bool {
	return strings.HasPrefix(strings.Trim(v, "\"'"), "[REDACTED:")
}

func genericSecretRule() rule {
	// A key is any identifier containing a sensitive word (db_password,
	// X-Api-Key, clientSecret), or one of a few exact names that would be too
	// common as substrings (auth, session, sid).
	ident := `[a-z0-9_.-]*`
	word := `(?:password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credential)`
	exact := `(?:auth(?:orization)?|session(?:[_-]?id)?|sid|jsessionid|phpsessid)`
	wordKey := ident + word + ident
	exactKey := `\b` + exact + `\b`
	key := `(?:` + wordKey + `|` + exactKey + `)`
	// A value is a quoted string (may contain spaces) or a bare run of
	// non-space characters. Unambiguous key names (password, secret, token)
	// redact even short bare values such as password=admin; the ambiguous
	// exact names (session, sid, auth) need 6+ so that `session:abc` in a
	// Redis log line is left alone.
	quoted := `"[^"\n]{1,}"|'[^'\n]{1,}'`
	valShort := `(` + quoted + `|[^\s"',;]{3,})`
	valLong := `(` + quoted + `|[^\s"',;]{6,})`
	mk := func(sep, k, val string) *regexp.Regexp {
		return regexp.MustCompile(`(?i)(\b` + k + `)(\s*` + sep + `\s*)` + val)
	}
	eqW, colonW := mk("=", wordKey, valShort), mk(":", wordKey, valShort)
	eqX, colonX := mk("=", exactKey, valLong), mk(":", exactKey, valLong)
	// JSON string values may contain escaped quotes; single-quoted dict keys
	// and values (Python reprs) are handled the same way.
	json := regexp.MustCompile(`(?i)("` + key + `"\s*:\s*")((?:[^"\\]|\\.)+)(")`)
	pyDict := regexp.MustCompile(`(?i)('` + key + `'\s*:\s*')((?:[^'\\]|\\.)+)(')`)
	token := "[REDACTED:generic-secret]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "generic-secret",
		kind: KindSecret,
		apply: func(s string) (string, int) {
			n := 0
			replPair := func(re *regexp.Regexp, in string) string {
				return re.ReplaceAllStringFunc(in, func(m string) string {
					sub := re.FindStringSubmatch(m)
					if len(sub) < 4 || alreadyRedacted(sub[3]) {
						return m
					}
					n++
					return sub[1] + sub[2] + token
				})
			}
			for _, re := range []*regexp.Regexp{eqW, colonW, eqX, colonX} {
				s = replPair(re, s)
			}
			for _, re := range []*regexp.Regexp{json, pyDict} {
				s = re.ReplaceAllStringFunc(s, func(m string) string {
					sub := re.FindStringSubmatch(m)
					if len(sub) < 4 || alreadyRedacted(sub[2]) {
						return m
					}
					n++
					return sub[1] + token + sub[3]
				})
			}
			return s, n
		},
	}
}

func ipv4Rule() rule {
	re := regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b`)
	token := "[REDACTED:ipv4]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "ipv4",
		kind: KindPII,
		apply: func(s string) (string, int) {
			idxs := re.FindAllStringIndex(s, -1)
			if len(idxs) == 0 {
				return s, 0
			}
			var b strings.Builder
			b.Grow(len(s))
			last, n := 0, 0
			for _, loc := range idxs {
				if isVersionContext(s, loc[0]) {
					continue
				}
				b.WriteString(s[last:loc[0]])
				b.WriteString(token)
				last = loc[1]
				n++
			}
			b.WriteString(s[last:])
			if n == 0 {
				return s, 0
			}
			return b.String(), n
		},
	}
}

// isVersionContext reports whether an IPv4-looking match at pos is a version string
// (preceded by 'v' or the word "version").
func isVersionContext(s string, pos int) bool {
	if pos > 0 {
		prev := s[pos-1]
		if prev == 'v' || prev == 'V' {
			return true
		}
	}
	// Look back for "version" as a whole word before optional whitespace.
	i := pos
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '=' || s[i-1] == ':') {
		i--
	}
	const word = "version"
	if i >= len(word) {
		frag := strings.ToLower(s[i-len(word) : i])
		if frag == word {
			if i-len(word) == 0 {
				return true
			}
			c := s[i-len(word)-1]
			if !isWordChar(c) {
				return true
			}
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

func ipv6Rule() rule {
	// Full and compressed forms with at least 3 colon-separated groups.
	re := regexp.MustCompile(`(?i)\b(?:(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{0,4}|(?:[0-9a-f]{1,4}:){1,5}(?::[0-9a-f]{1,4}){1,2}|(?:[0-9a-f]{1,4}:){1,4}(?::[0-9a-f]{1,4}){1,3}|(?:[0-9a-f]{1,4}:){1,3}(?::[0-9a-f]{1,4}){1,4}|(?:[0-9a-f]{1,4}:){1,2}(?::[0-9a-f]{1,4}){1,5}|[0-9a-f]{1,4}:(?::[0-9a-f]{1,4}){1,6}|:(?::[0-9a-f]{1,4}){2,6}|::(?:[0-9a-f]{1,4}:){0,5}[0-9a-f]{1,4}|[0-9a-f]{1,4}::(?:[0-9a-f]{1,4}:){0,4}[0-9a-f]{1,4})\b`)
	token := "[REDACTED:ipv6]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "ipv6",
		kind: KindPII,
		apply: func(s string) (string, int) {
			n := 0
			out := re.ReplaceAllStringFunc(s, func(m string) string {
				if strings.Count(m, ":") < 2 {
					return m
				}
				// Require at least 3 groups (2+ colons in non-degenerate form).
				groups := 0
				for _, p := range strings.Split(m, ":") {
					if p != "" {
						groups++
					}
				}
				if groups < 3 && !strings.Contains(m, "::") {
					return m
				}
				if groups < 2 {
					return m
				}
				n++
				return token
			})
			return out, n
		},
	}
}

var (
	phoneIntlRe = regexp.MustCompile(`\+[1-9]\d{0,2}(?:[\s\-.]?\(?\d{1,4}\)?)+`)
	// US/national forms require separators or parentheses so bare epochs/ids do not match.
	phoneUSRe = regexp.MustCompile(`(?:\(\d{3}\)[\s\-.]?|\d{3}[\s\-.])\d{3}[\s\-.]\d{4}`)
)

func phoneRule() rule {
	token := "[REDACTED:phone]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "phone",
		kind: KindPII,
		apply: func(s string) (string, int) {
			type loc struct{ a, b int }
			var cands []loc
			for _, re := range []*regexp.Regexp{phoneIntlRe, phoneUSRe} {
				for _, ix := range re.FindAllStringIndex(s, -1) {
					cands = append(cands, loc{ix[0], ix[1]})
				}
			}
			if len(cands) == 0 {
				return s, 0
			}
			// Longer matches first so overlapping candidates resolve stably.
			for i := 0; i < len(cands); i++ {
				for j := i + 1; j < len(cands); j++ {
					if cands[j].b-cands[j].a > cands[i].b-cands[i].a {
						cands[i], cands[j] = cands[j], cands[i]
					}
				}
			}
			covered := make([]bool, len(s))
			n := 0
			for _, c := range cands {
				overlap := false
				for i := c.a; i < c.b; i++ {
					if covered[i] {
						overlap = true
						break
					}
				}
				if overlap {
					continue
				}
				m := s[c.a:c.b]
				if !looksLikePhone(m, s, c.a) {
					continue
				}
				for i := c.a; i < c.b; i++ {
					covered[i] = true
				}
				n++
			}
			if n == 0 {
				return s, 0
			}
			var b strings.Builder
			b.Grow(len(s))
			for i := 0; i < len(s); {
				if !covered[i] {
					b.WriteByte(s[i])
					i++
					continue
				}
				b.WriteString(token)
				for i < len(s) && covered[i] {
					i++
				}
			}
			return b.String(), n
		},
	}
}

func looksLikePhone(m, full string, pos int) bool {
	digits := countDigits(m)
	if digits < 9 || digits > 15 {
		return false
	}
	// UUID / hex-id fragment: digit run preceded by a hex letter.
	if pos > 0 {
		prev := full[pos-1]
		if isHexLetter(prev) {
			return false
		}
	}
	// Reject dotted version numbers.
	if strings.Count(m, ".") >= 2 && !strings.ContainsAny(m, "+()-") {
		onlyVer := true
		for _, r := range m {
			if !unicode.IsDigit(r) && r != '.' {
				onlyVer = false
				break
			}
		}
		if onlyVer {
			return false
		}
	}
	// Reject ISO-ish timestamps / dates.
	if strings.Contains(m, "T") {
		return false
	}
	parts := splitPhoneParts(m)
	if len(parts) == 3 && len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2 {
		return false
	}
	trimmed := strings.TrimSpace(m)
	if strings.HasPrefix(trimmed, "+") {
		return true
	}
	// US-style only when not embedded in a longer digit/hex token.
	if phoneUSRe.MatchString(trimmed) {
		if pos+len(m) < len(full) {
			next := full[pos+len(m)]
			if isHexLetter(next) || (next >= '0' && next <= '9') {
				return false
			}
		}
		return true
	}
	return false
}

func isHexLetter(c byte) bool {
	return (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func splitPhoneParts(m string) []string {
	var parts []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	}
	for _, r := range m {
		if unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else if r == '-' || r == ' ' || r == '.' {
			flush()
		}
	}
	flush()
	return parts
}

func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

func creditCardRule() rule {
	// 13–19 digits with optional spaces/dashes between digit groups.
	re := regexp.MustCompile(`(?:\d[ -]*){12,18}\d`)
	token := "[REDACTED:credit-card]" //nolint:gosec // G101: a placeholder, not a credential
	return rule{
		name: "credit-card",
		kind: KindPII,
		apply: func(s string) (string, int) {
			idxs := re.FindAllStringIndex(s, -1)
			if len(idxs) == 0 {
				return s, 0
			}
			var b strings.Builder
			b.Grow(len(s))
			last, n := 0, 0
			for _, loc := range idxs {
				if loc[0] < last {
					continue
				}
				m := s[loc[0]:loc[1]]
				digits := digitsOnly(m)
				if len(digits) < 13 || len(digits) > 19 {
					continue
				}
				if !luhnValid(digits) {
					continue
				}
				b.WriteString(s[last:loc[0]])
				b.WriteString(token)
				last = loc[1]
				n++
			}
			b.WriteString(s[last:])
			if n == 0 {
				return s, 0
			}
			return b.String(), n
		},
	}
}

func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func luhnValid(digits string) bool {
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}
