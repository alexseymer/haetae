package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type openAI struct {
	name, base, key, model string
	local                  bool
	client                 *http.Client
}

// NewOpenAICompat returns a backend for OpenAI-style chat completions. With
// cfg.Kind "local" the key is optional and Local() depends on the host.
func NewOpenAICompat(cfg Config) (Provider, error) {
	if cfg.Kind != "local" {
		cfg.Kind = "openai"
	}
	base, local, err := resolveBase(cfg, "https://api.openai.com/v1")
	if err != nil {
		return nil, err
	}
	return &openAI{name: cfg.Kind, base: base, key: cfg.APIKey, model: cfg.Model,
		local: cfg.Kind == "local" && local, client: newClient(cfg)}, nil
}

func (o *openAI) Name() string { return o.name }
func (o *openAI) Local() bool  { return o.local }

type oaFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   *string         `json:"arguments,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function oaFunc `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function oaFunc `json:"function"`
}

type oaRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	Tools     []oaTool    `json:"tools,omitempty"`
	MaxTokens int         `json:"max_tokens"`
}

type oaResponse struct {
	Choices []struct {
		Message oaMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func strp(s string) *string { return &s }

func (o *openAI) Complete(ctx context.Context, req Request) (Response, error) {
	body := oaRequest{Model: o.model, MaxTokens: clampMaxTokens(req.MaxTokens), Messages: []oaMessage{}}
	if req.System != "" {
		body.Messages = append(body.Messages, oaMessage{Role: "system", Content: strp(req.System)})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			body.Messages = append(body.Messages, oaMessage{Role: "user", Content: strp(m.Text)})
		case RoleAssistant:
			msg := oaMessage{Role: "assistant"}
			if m.Text != "" || len(m.ToolCalls) == 0 {
				msg.Content = strp(m.Text)
			}
			for _, tc := range m.ToolCalls {
				args := string(rawOrEmptyObject(tc.Args))
				msg.ToolCalls = append(msg.ToolCalls, oaToolCall{ID: tc.ID, Type: "function",
					Function: oaFunc{Name: tc.Name, Arguments: &args}})
			}
			body.Messages = append(body.Messages, msg)
		case RoleTool:
			body.Messages = append(body.Messages, oaMessage{Role: "tool", ToolCallID: m.ToolCallID, Content: strp(m.Text)})
		default:
			return Response{}, fmt.Errorf("%w: unknown message role %q", ErrBadResponse, m.Role)
		}
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, oaTool{Type: "function",
			Function: oaFunc{Name: t.Name, Description: t.Description, Parameters: schemaOrObject(t.Schema)}})
	}

	headers := map[string]string{}
	if o.key != "" {
		headers["Authorization"] = "Bearer " + o.key
	}
	var out oaResponse
	if err := doJSON(ctx, o.client, o.base+"/chat/completions", headers, o.key, body, &out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("%w: no choices", ErrBadResponse)
	}
	msg := out.Choices[0].Message
	var resp Response
	if msg.Content != nil {
		resp.Text = *msg.Content
	}
	for _, tc := range msg.ToolCalls {
		args := ""
		if tc.Function.Arguments != nil {
			args = *tc.Function.Arguments
		}
		raw := json.RawMessage(args)
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if !json.Valid(raw) {
			return Response{}, fmt.Errorf("%w: tool call %q has invalid JSON arguments", ErrBadResponse, tc.Function.Name)
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: raw})
	}
	resp.Usage = Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	return resp, nil
}
