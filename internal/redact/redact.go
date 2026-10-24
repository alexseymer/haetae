// Package redact removes secrets and personal data from log lines on the host
// before anything leaves it. Redaction is fail-closed: if it cannot complete,
// the caller must send nothing.
package redact

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind classifies what a rule removes.
type Kind string

const (
	// KindSecret marks credential and secret material.
	KindSecret Kind = "secret"
	// KindPII marks personal or identifying data.
	KindPII Kind = "pii"
)

// MaxLineBytes is the largest line Redact will process.
const MaxLineBytes = 64 << 10

// ErrBudget is returned when the time budget ran out before every line was redacted.
var ErrBudget = errors.New("redact: time budget exceeded")

// ErrLineTooLong is returned when a line exceeds MaxLineBytes.
var ErrLineTooLong = errors.New("redact: line too long")

// Redactor applies all rules. Safe for concurrent use.
type Redactor struct {
	rules []rule
}

// New returns a Redactor with the built-in secret and PII rules.
func New() *Redactor {
	return &Redactor{rules: defaultRules()}
}

// newWithRules builds a Redactor from an explicit rule list (tests only).
func newWithRules(rules []rule) *Redactor {
	return &Redactor{rules: rules}
}

// RuleInfo describes one installed rule.
type RuleInfo struct {
	// Name is the stable rule identifier used in [REDACTED:<name>] tokens.
	Name string
	// Kind is secret or pii.
	Kind Kind
}

// Rules returns names and kinds of all rules (for docs and tests).
func (r *Redactor) Rules() []RuleInfo {
	out := make([]RuleInfo, len(r.rules))
	for i, ru := range r.rules {
		out[i] = RuleInfo{Name: ru.name, Kind: ru.kind}
	}
	return out
}

// Result is the outcome for a batch.
type Result struct {
	// Lines are the redacted inputs, same order and length as the request.
	Lines []string
	// Counts maps rule name to the number of replacements performed.
	Counts map[string]int
}

// Redact returns line with every match replaced by "[REDACTED:<name>]" where
// <name> is the rule name (e.g. "aws-access-key", "email"). Lines longer than
// MaxLineBytes return ErrLineTooLong (never silently pass through).
func (r *Redactor) Redact(line string) (string, error) {
	if len(line) > MaxLineBytes {
		return "", ErrLineTooLong
	}
	out, _, err := r.applyAll(line, nil)
	if err != nil {
		return "", err
	}
	return out, nil
}

// RedactBatch redacts all lines. It checks ctx and a monotonic deadline of budget
// between lines AND between rules; if either expires, or any line errors, or a
// rule panics (recover it), it returns (Result{}, err) with NO partial output:
// the caller then sends nothing. Counts maps rule name -> number of replacements.
func (r *Redactor) RedactBatch(ctx context.Context, lines []string, budget time.Duration) (Result, error) {
	deadline := time.Now().Add(budget)
	check := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("redact: context: %w", err)
		}
		if time.Now().After(deadline) {
			return ErrBudget
		}
		return nil
	}
	if err := check(); err != nil {
		return Result{}, err
	}

	out := make([]string, 0, len(lines))
	counts := make(map[string]int)

	for _, line := range lines {
		if err := check(); err != nil {
			return Result{}, err
		}
		redacted, lineCounts, err := r.applyAll(line, check)
		if err != nil {
			return Result{}, err
		}
		out = append(out, redacted)
		for name, n := range lineCounts {
			counts[name] += n
		}
	}
	return Result{Lines: out, Counts: counts}, nil
}

// budgetCheck is called between rules; nil means no budget/context checks.
type budgetCheck func() error

func (r *Redactor) applyAll(line string, check budgetCheck) (string, map[string]int, error) {
	if len(line) > MaxLineBytes {
		return "", nil, ErrLineTooLong
	}
	counts := make(map[string]int)
	s := line
	for _, ru := range r.rules {
		if check != nil {
			if err := check(); err != nil {
				return "", nil, err
			}
		}
		next, n, err := applyRule(ru, s)
		if err != nil {
			return "", nil, err
		}
		s = next
		if n > 0 {
			counts[ru.name] += n
		}
	}
	return s, counts, nil
}

func applyRule(ru rule, s string) (out string, n int, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			out = ""
			n = 0
			err = fmt.Errorf("redact: rule %q panic: %v", ru.name, rec)
		}
	}()
	out, n = ru.apply(s)
	return out, n, nil
}
