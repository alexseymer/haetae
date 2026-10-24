package redact

import "strings"

// maxPEMLines bounds how many lines after a BEGIN header are blanked, so a
// header without a trailer cannot swallow the rest of a log forever.
const maxPEMLines = 200

const pemPlaceholder = "[REDACTED:private-key]" //nolint:gosec // G101: a placeholder, not a credential

// PEMGuard blanks the continuation lines of a private key that a program
// logged over several lines. Docker delivers one line at a time and the rules
// work on single lines, so lines 2..N of a PEM body (plain base64, no header)
// would otherwise pass every rule. Feed lines in order through Filter before
// they are stored or aggregated. Not safe for concurrent use.
type PEMGuard struct {
	inBlock bool
	lines   int
}

// Filter returns line unchanged, or the placeholder when line lies inside a
// private key block. The BEGIN line itself is returned unchanged: the
// private-key rule redacts it later.
func (g *PEMGuard) Filter(line string) string {
	if g.inBlock {
		g.lines++
		if (strings.Contains(line, "-----END ") && strings.Contains(line, "PRIVATE KEY")) || g.lines > maxPEMLines {
			g.inBlock = false
		}
		return pemPlaceholder
	}
	if i := strings.Index(line, "-----BEGIN "); i >= 0 {
		rest := line[i:]
		// "PRIVATE KEY-----" (PEM) and "PRIVATE KEY BLOCK-----" (PGP).
		if strings.Contains(rest, "PRIVATE KEY") && !strings.Contains(rest, "-----END ") {
			g.inBlock, g.lines = true, 0
		}
	}
	return line
}
