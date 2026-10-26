package briefing

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

// RewriteTimeout bounds one AI rewrite. The briefing is sent with the rule
// text when it elapses.
const RewriteTimeout = 20 * time.Second

// Rewriter is the optional AI that rewords the rule-composed briefing. It is
// the part of operator.Operator the scheduler uses.
type Rewriter interface {
	Available(ctx context.Context, orgID uuid.UUID) (ok bool, reason string)
	RewriteBriefing(ctx context.Context, orgID uuid.UUID, facts operator.BriefingFacts, ruleText string) (string, error)
}

// choose returns the AI text when a rewrite is available and acceptable, else
// ruleText. It never returns an error: every failure means the rule text.
func (s *Scheduler) choose(ctx context.Context, orgID uuid.UUID, facts store.BriefingFacts, ruleText string) string {
	rw := s.Rewriter
	if rw == nil {
		return ruleText
	}
	timeout := s.RewriteTimeout
	if timeout <= 0 {
		timeout = RewriteTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		text string
		ok   bool
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{}
			}
		}()
		if ok, _ := rw.Available(cctx, orgID); !ok {
			ch <- result{}
			return
		}
		text, err := rw.RewriteBriefing(cctx, orgID, briefingFacts(facts), ruleText)
		if err != nil {
			ch <- result{}
			return
		}
		ch <- result{text, true}
	}()
	select {
	case r := <-ch:
		if r.ok && acceptable(ruleText, r.text) {
			return r.text
		}
	case <-cctx.Done():
	}
	return ruleText
}

// briefingFacts derives the operator's facts from what Compose computes.
func briefingFacts(f store.BriefingFacts) operator.BriefingFacts {
	total, running := 0, 0
	for state, n := range f.Containers {
		total += n
		if state == "running" {
			running += n
		}
	}
	out := operator.BriefingFacts{Hosts: f.Hosts, Containers: total, Running: running}
	for i, it := range rankItems(f.Items) {
		if i >= maxLines-1 {
			break
		}
		out.Items = append(out.Items, formatItem(it))
	}
	return out
}

// acceptable reports whether aiText may replace ruleText: at most 3 lines,
// 300 characters, no control characters, and every item of the rule text kept.
func acceptable(ruleText, aiText string) bool {
	aiText = strings.TrimSpace(aiText)
	if aiText == "" || utf8.RuneCountInString(aiText) > maxChars {
		return false
	}
	if len(strings.Split(aiText, "\n")) > maxLines {
		return false
	}
	for _, r := range aiText {
		if r != '\n' && unicode.IsControl(r) {
			return false
		}
	}
	return briefingPreserves(ruleText, aiText)
}

// fixedWords is the vocabulary Compose writes around the facts; it carries no
// information the rewrite must keep.
var fixedWords = map[string]bool{
	"briefing": true, "hosts": true, "containers": true, "all": true,
	"running": true, "offline": true, "not": true, "on": true,
}

// briefingPreserves reports whether every number and every identifier of
// ruleText (host and container names, titles) also appears in aiText. A token
// is a maximal run of letters, digits and . _ : / -; the words Compose adds
// itself are exempt. Numbers must match as whole numbers ("2" is not found in
// "12"); identifiers match as substrings.
func briefingPreserves(ruleText, aiText string) bool {
	aiTokens := map[string]bool{}
	for _, t := range tokens(aiText) {
		aiTokens[t] = true
	}
	for _, t := range tokens(ruleText) {
		if fixedWords[strings.ToLower(t)] {
			continue
		}
		if isNumber(t) {
			if !aiTokens[t] && !containsNumber(aiText, t) {
				return false
			}
			continue
		}
		if !strings.Contains(aiText, t) {
			return false
		}
	}
	return stateWordsKept(ruleText, aiText) && noNewFacts(ruleText, aiText)
}

// stateWords are the words that carry a status. A rewrite that drops one
// ("2 hosts offline" becoming "2 hosts online") could invert a fact.
var stateWords = map[string]bool{
	"offline": true, "down": true, "not": true, "failed": true, "unhealthy": true,
	"restarting": true, "stopped": true, "exited": true, "critical": true, "high": true,
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		out[w] = true
	}
	return out
}

// stateWordsKept reports whether every state word of the rule text is also in
// the AI text.
func stateWordsKept(ruleText, aiText string) bool {
	ai := words(aiText)
	for w := range words(ruleText) {
		if stateWords[w] && !ai[w] {
			return false
		}
	}
	return true
}

// noNewFacts reports whether the AI text introduces no number (maximal digit
// run) and no identifier-like token (one with a digit or one of . _ : / -)
// that the rule text does not contain.
func noNewFacts(ruleText, aiText string) bool {
	ruleNums := map[string]bool{}
	for _, n := range digitRuns(ruleText) {
		ruleNums[n] = true
	}
	for _, n := range digitRuns(aiText) {
		if !ruleNums[n] {
			return false
		}
	}
	for _, t := range tokens(aiText) {
		if isNumber(t) || !strings.ContainsAny(t, "._:/-0123456789") {
			continue
		}
		if !strings.Contains(ruleText, t) {
			return false
		}
	}
	return true
}

func digitRuns(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) })
}

func tokens(s string) []string {
	f := func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._:/-", r)
	}
	var out []string
	for _, t := range strings.FieldsFunc(s, f) {
		t = strings.Trim(t, "._:/-")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func isNumber(t string) bool {
	for _, r := range t {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return t != ""
}

// containsNumber reports whether s has n as a whole run of digits.
func containsNumber(s, n string) bool {
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) {
		if t == n {
			return true
		}
	}
	return false
}
