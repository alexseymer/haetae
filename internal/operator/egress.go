package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/redact"
)

// egressBudget is the time one batch of strings may spend in redaction.
const egressBudget = 5 * time.Second

// defaultRedact is the real redaction pipeline, one string per call with a
// per-call time budget.
func defaultRedact() func(string) (string, error) {
	r := redact.New()
	return func(s string) (string, error) {
		res, err := r.RedactBatch(context.Background(), []string{s}, egressBudget)
		if err != nil {
			return "", err
		}
		if len(res.Lines) != 1 {
			return "", fmt.Errorf("redact: unexpected result of %d lines", len(res.Lines))
		}
		return res.Lines[0], nil
	}
}

// Egress prepares strings for a provider call. For a non-local provider every
// string is passed through redact; on any error, panic, cancelled context or
// exceeded batch budget it returns ErrRedaction and no output, and the caller
// must send nothing. For a local provider the strings are returned as they are
// (the host already redacted logs before they left it).
//
// The fixed system prompt is not passed through here.
func Egress(ctx context.Context, p provider.Provider, redactFn func(string) (string, error), in []string) ([]string, error) {
	out := make([]string, len(in))
	if p != nil && p.Local() {
		copy(out, in)
		return out, nil
	}
	if redactFn == nil {
		return nil, fmt.Errorf("%w: no redactor", ErrRedaction)
	}
	deadline := time.Now().Add(egressBudget)
	for i, s := range in {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRedaction, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: time budget exceeded", ErrRedaction)
		}
		r, err := safeRedact(redactFn, s)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRedaction, err)
		}
		out[i] = r
	}
	return out, nil
}

func safeRedact(f func(string) (string, error), s string) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("redactor panic: %v", r)
		}
	}()
	return f(s)
}

// egressMessage passes the text and the tool-call arguments of a message
// through Egress.
func (s *Service) egressMessage(ctx context.Context, m provider.Message) (provider.Message, error) {
	in := make([]string, 0, 1+len(m.ToolCalls))
	in = append(in, m.Text)
	for _, tc := range m.ToolCalls {
		in = append(in, string(tc.Args))
	}
	out, err := Egress(ctx, s.cfg.Provider, s.cfg.Redact, in)
	if err != nil {
		return provider.Message{}, err
	}
	m.Text = out[0]
	if len(m.ToolCalls) > 0 {
		calls := make([]provider.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			tc.Args = json.RawMessage(out[i+1])
			if !json.Valid(tc.Args) {
				tc.Args = json.RawMessage(`{}`)
			}
			calls[i] = tc
		}
		m.ToolCalls = calls
	}
	return m, nil
}
