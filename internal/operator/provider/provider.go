// Package provider is the only code that talks to an AI service. Everything
// above it sees the Provider interface, typed errors and token usage.
package provider

import (
	"context"
	"encoding/json"
	"errors"
)

// Role of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // a tool result answering ToolCallID
)

// Message is one turn. An assistant message may carry tool calls; a tool
// message carries the result of one call.
type Message struct {
	Role       Role
	Text       string
	ToolCalls  []ToolCall // assistant only
	ToolCallID string     // tool only
}

// ToolSpec describes one tool the model may call. Schema is a JSON Schema
// object for the arguments.
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Request is one model call.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int
}

// Usage is the token count the provider reported for a call.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Response is the model's answer: text, tool calls, or both.
type Response struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
}

// Provider is one AI backend.
type Provider interface {
	// Name is a short label for logs ("anthropic", "openai", "local", "fake").
	Name() string
	// Local reports whether the endpoint is operated by the user (loopback or
	// a private address). Non-local providers only ever receive redacted text.
	Local() bool
	Complete(ctx context.Context, req Request) (Response, error)
}

// Typed errors. Callers map every one of them to the rule-based fallback.
var (
	ErrUnavailable = errors.New("provider unavailable")
	ErrRateLimited = errors.New("provider rate limited")
	ErrAuth        = errors.New("provider rejected the credentials")
	ErrBadResponse = errors.New("provider returned an unusable response")
)
