package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type anthropic struct {
	base, key, model string
	client           *http.Client
}

// NewAnthropic returns a backend for the Anthropic Messages API.
func NewAnthropic(cfg Config) (Provider, error) {
	cfg.Kind = "anthropic"
	base, _, err := resolveBase(cfg, "https://api.anthropic.com")
	if err != nil {
		return nil, err
	}
	return &anthropic{base: base, key: cfg.APIKey, model: cfg.Model, client: newClient(cfg)}, nil
}

func (a *anthropic) Name() string { return "anthropic" }
func (a *anthropic) Local() bool  { return false }

type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anRequest struct {
	Model     string      `json:"model"`
	MaxTokens int         `json:"max_tokens"`
	System    string      `json:"system,omitempty"`
	Messages  []anMessage `json:"messages"`
	Tools     []anTool    `json:"tools,omitempty"`
}

type anResponse struct {
	Content []anBlock `json:"content"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (a *anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	body := anRequest{Model: a.model, MaxTokens: clampMaxTokens(req.MaxTokens), System: req.System, Messages: []anMessage{}}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleAssistant:
			msg := anMessage{Role: "assistant"}
			if m.Text != "" {
				msg.Content = append(msg.Content, anBlock{Type: "text", Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				msg.Content = append(msg.Content, anBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: rawOrEmptyObject(tc.Args)})
			}
			if len(msg.Content) == 0 {
				msg.Content = []anBlock{{Type: "text", Text: " "}}
			}
			body.Messages = append(body.Messages, msg)
		case RoleTool:
			blk := anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Text}
			if n := len(body.Messages); n > 0 && body.Messages[n-1].Role == "user" && isToolResults(body.Messages[n-1]) {
				body.Messages[n-1].Content = append(body.Messages[n-1].Content, blk)
			} else {
				body.Messages = append(body.Messages, anMessage{Role: "user", Content: []anBlock{blk}})
			}
		case RoleUser:
			body.Messages = append(body.Messages, anMessage{Role: "user", Content: []anBlock{{Type: "text", Text: m.Text}}})
		default:
			return Response{}, fmt.Errorf("%w: unknown message role %q", ErrBadResponse, m.Role)
		}
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anTool{Name: t.Name, Description: t.Description, InputSchema: schemaOrObject(t.Schema)})
	}

	var out anResponse
	headers := map[string]string{"x-api-key": a.key, "anthropic-version": "2023-06-01"}
	if err := doJSON(ctx, a.client, a.base+"/v1/messages", headers, a.key, body, &out); err != nil {
		return Response{}, err
	}
	var resp Response
	var text strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: rawOrEmptyObject(b.Input)})
		}
	}
	resp.Text = text.String()
	resp.Usage = Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	return resp, nil
}

func isToolResults(m anMessage) bool {
	return len(m.Content) > 0 && m.Content[0].Type == "tool_result"
}
