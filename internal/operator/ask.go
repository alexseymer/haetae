package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

const (
	maxRounds      = 12
	maxQuestionLen = 4000 // bytes
	answerTokens   = 1500
)

// Ask implements Operator.
func (s *Service) Ask(ctx context.Context, orgID uuid.UUID, question string) (Answer, error) {
	if err := s.gate(ctx, orgID); err != nil {
		return Answer{}, err
	}
	question = strings.TrimSpace(truncateBytes(question, maxQuestionLen))
	if question == "" {
		return Answer{}, errors.New("operator: empty question")
	}
	c := &conv{id: uuid.New(), org: orgID}
	first, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleUser, Text: question})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	return s.runConv(ctx, c, []provider.Message{first}, true)
}

// runConv runs the tool loop of a conversation from the (already egressed)
// first messages to a validated Answer. With allowActions false, proposed
// actions in the model output are ignored.
func (s *Service) runConv(ctx context.Context, c *conv, msgs []provider.Message, allowActions bool) (Answer, error) {
	orgID := c.org
	tools := toolSpecs()

	for round := 0; round < maxRounds; round++ {
		offer := tools
		if c.calls >= maxToolCalls {
			// The budget is spent: the model must answer. With nothing
			// gathered there is nothing to answer from, so do not ask.
			offer = nil
			if len(c.evs) == 0 {
				return s.notFound(c), nil
			}
		}
		resp, err := s.complete(ctx, orgID, provider.Request{System: systemPrompt, Messages: msgs, Tools: offer, MaxTokens: answerTokens})
		if err != nil {
			return Answer{ConversationID: c.id}, err
		}

		if len(resp.ToolCalls) > 0 && offer != nil {
			am, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleAssistant, Text: resp.Text, ToolCalls: withIDs(resp.ToolCalls, round)})
			if err != nil {
				return Answer{ConversationID: c.id}, err
			}
			msgs = append(msgs, am)
			for _, tc := range am.ToolCalls {
				var result string
				if c.calls >= maxToolCalls {
					result = `{"error":"tool call limit reached; answer now from the evidence you have"}`
				} else {
					result, _ = s.callTool(ctx, c, tc.Name, tc.Args)
				}
				tm, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleTool, ToolCallID: tc.ID, Text: result})
				if err != nil {
					return Answer{ConversationID: c.id}, err
				}
				msgs = append(msgs, tm)
			}
			continue
		}

		// A final text.
		if len(c.evs) == 0 {
			return s.notFound(c), nil
		}
		w, perr := parseAnswer(resp.Text)
		if perr != nil {
			bad, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleAssistant, Text: truncateBytes(resp.Text, 2000)})
			if err != nil {
				return Answer{ConversationID: c.id}, err
			}
			fix, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleUser, Text: repairPrompt})
			if err != nil {
				return Answer{ConversationID: c.id}, err
			}
			msgs = append(msgs, bad, fix)
			// The repair request offers no tools, so it cannot gather more.
			resp2, err := s.complete(ctx, orgID, provider.Request{System: systemPrompt, Messages: msgs, MaxTokens: answerTokens})
			if err != nil {
				return Answer{ConversationID: c.id}, err
			}
			w, perr = parseAnswer(resp2.Text)
			if perr != nil {
				return Answer{ConversationID: c.id}, perr
			}
		}
		return s.finish(ctx, c, w, allowActions), nil
	}
	return Answer{ConversationID: c.id}, fmt.Errorf("%w: the model did not answer", ErrBadResponse)
}

func (s *Service) notFound(c *conv) Answer {
	return Answer{ConversationID: c.id, CauseNotFound: true}
}

// finish validates the parsed answer and decides on its proposed actions.
func (s *Service) finish(ctx context.Context, c *conv, w wireAnswer, allowActions bool) Answer {
	kept, dropped, cited := validate(c, w.Statements)
	ans := Answer{ConversationID: c.id, Statements: kept, Evidence: cited, DroppedCount: dropped}
	if len(kept) == 0 {
		ans.CauseNotFound = true
		ans.Statements, ans.Evidence = nil, nil
	}
	for i, a := range w.Actions {
		if !allowActions || i >= maxActionsPerAns {
			break
		}
		p := ProposedAction{
			Kind:        strings.TrimSpace(a.Kind),
			ContainerID: strings.TrimSpace(a.ContainerID),
			Reason:      cleanText(a.Reason, maxReasonLen),
			Evidence:    validIDs(c, a.Evidence),
		}
		ans.Actions = append(ans.Actions, s.decide(ctx, c, p))
	}
	return ans
}

func withIDs(tcs []provider.ToolCall, round int) []provider.ToolCall {
	out := make([]provider.ToolCall, len(tcs))
	for i, tc := range tcs {
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d_%d", round, i)
		}
		out[i] = tc
	}
	return out
}

// complete makes one provider call: it checks the usage cap first, bounds the
// call with CallTimeout and records the usage afterwards (estimated from text
// length when the provider reports none, so the cap still bites).
func (s *Service) complete(ctx context.Context, orgID uuid.UUID, req provider.Request) (provider.Response, error) {
	if err := s.gate(ctx, orgID); err != nil {
		return provider.Response{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	resp, err := s.cfg.Provider.Complete(cctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return provider.Response{}, ctx.Err()
		}
		return provider.Response{}, fmt.Errorf("operator: provider: %w", err)
	}
	in, out := resp.Usage.InputTokens, resp.Usage.OutputTokens
	if in == 0 && out == 0 {
		n := len(req.System)
		for _, m := range req.Messages {
			n += len(m.Text)
			for _, tc := range m.ToolCalls {
				n += len(tc.Args)
			}
		}
		in = int64(n / 4)
		o := len(resp.Text)
		for _, tc := range resp.ToolCalls {
			o += len(tc.Args) + len(tc.Name)
		}
		out = int64(o / 4)
		if in+out == 0 {
			in = 1
		}
	}
	s.recordUsage(ctx, orgID, in, out)
	return resp, nil
}
