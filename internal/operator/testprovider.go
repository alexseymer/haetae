package operator

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

// testTimeout bounds each of the two requests of TestProvider.
const testTimeout = 15 * time.Second

// Values of TestResult.ToolCalling.
const (
	ToolsSupported = "supported"
	ToolsNotUsed   = "not used by this model"
	ToolsUnknown   = "unknown"
)

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK          bool   `json:"ok"`
	LatencyMS   int64  `json:"latencyMs"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Local       bool   `json:"local"`
	ToolCalling string `json:"toolCalling"`
	Class       string `json:"class,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Hint        string `json:"hint,omitempty"`
}

// TestProvider checks the configured provider with two fixed requests that
// carry no user data: a one-word reply, then a request offering a trivial
// ping tool to learn whether the model calls tools. It runs whether or not
// the org enabled the operator and is not blocked by the daily cap, but the
// usage is recorded. A provider failure is reported in the result, not as an
// error; the error is ErrDisabled when no provider is configured.
func (s *Service) TestProvider(ctx context.Context, orgID uuid.UUID) (TestResult, error) {
	p := s.cfg.Provider
	if p == nil || s.cfg.Store == nil {
		return TestResult{}, joinErr(ErrDisabled, errNotConfigured)
	}
	res := TestResult{Provider: p.Name(), Model: provider.ModelOf(p), Local: p.Local(), ToolCalling: ToolsUnknown}
	fail := func(err error) (TestResult, error) {
		f := Classify(err)
		s.logFailure("test", err)
		res.OK, res.Class, res.Reason, res.Hint = false, f.Class, f.Reason, f.Hint
		return res, nil
	}

	start := time.Now()
	_, err := s.call(ctx, orgID, provider.Request{
		System:    "You are a connection test.",
		Messages:  []provider.Message{{Role: provider.RoleUser, Text: "Reply with the single word OK."}},
		MaxTokens: 16,
	}, testTimeout)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fail(err)
	}
	res.OK = true

	pingSchema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	resp, err := s.call(ctx, orgID, provider.Request{
		System:    "You are a connection test. Call the ping tool.",
		Messages:  []provider.Message{{Role: provider.RoleUser, Text: "Call the ping tool now."}},
		Tools:     []provider.ToolSpec{{Name: "ping", Description: "Returns pong. Takes no arguments.", Schema: pingSchema}},
		MaxTokens: 64,
	}, testTimeout)
	switch {
	case err == nil:
		res.ToolCalling = ToolsNotUsed
		for _, tc := range resp.ToolCalls {
			if tc.Name == "ping" {
				res.ToolCalling = ToolsSupported
			}
		}
	case Classify(err).Class == ClassBadRequest:
		// The plain request worked, the one with tools was refused: the
		// provider says this model has no tool support.
		res.ToolCalling = ToolsNotUsed
		f := Classify(err)
		res.Class, res.Reason, res.Hint = f.Class, f.Reason, f.Hint
		s.logFailure("test", err)
	default:
		return fail(err)
	}
	return res, nil
}
