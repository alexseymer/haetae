//go:build screenshots

package screenshots_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// oaMsg is one message of an OpenAI-format chat completion request.
type oaMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func (m oaMsg) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	return string(m.Content)
}

type call struct {
	Name string
	Args any
}

type reply struct {
	Text  string
	Calls []call
}

// scriptFn decides the reply to one request (first user message, all messages).
type scriptFn func(first string, msgs []oaMsg) reply

// provider is a scripted OpenAI-compatible /v1/chat/completions endpoint.
type provider struct {
	srv *httptest.Server
	mu  sync.Mutex
	fn  scriptFn
}

func startProvider(t *testing.T) *provider {
	p := &provider{}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) URL() string { return p.srv.URL }

func (p *provider) setScript(fn scriptFn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fn = fn
}

// toolResults returns the parsed tool results keyed by tool name.
func toolResults(msgs []oaMsg) map[string]map[string]any {
	names := map[string]string{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Function.Name
		}
	}
	out := map[string]map[string]any{}
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		var v map[string]any
		if json.Unmarshal([]byte(m.text()), &v) == nil {
			out[names[m.ToolCallID]] = v
		}
	}
	return out
}

func evidenceID(msgs []oaMsg, tool string) string {
	id, _ := toolResults(msgs)[tool]["evidence_id"].(string)
	return id
}

func (p *provider) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []oaMsg `json:"messages"`
	}
	_ = json.Unmarshal(raw, &body)
	first := ""
	for _, m := range body.Messages {
		if m.Role == "user" {
			first = m.text()
			break
		}
	}
	if os.Getenv("SHOT_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "PROVIDER REQ: %s\n", raw)
	}
	p.mu.Lock()
	fn := p.fn
	p.mu.Unlock()
	rep := reply{Text: `{"statements":[],"actions":[]}`}
	if fn != nil {
		rep = fn(first, body.Messages)
	}
	msg := map[string]any{"role": "assistant", "content": rep.Text}
	if len(rep.Calls) > 0 {
		var tcs []map[string]any
		for i, c := range rep.Calls {
			args, _ := json.Marshal(c.Args)
			tcs = append(tcs, map[string]any{
				"id": fmt.Sprintf("call_%d_%d", len(body.Messages), i), "type": "function",
				"function": map[string]any{"name": c.Name, "arguments": string(args)},
			})
		}
		msg["tool_calls"] = tcs
		msg["content"] = nil
	}
	// Plausible token counts so "usage today" looks like real traffic.
	out := len(rep.Text)/4 + 40*len(rep.Calls)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
		"usage": map[string]any{"prompt_tokens": len(raw) / 4, "completion_tokens": out,
			"total_tokens": len(raw)/4 + out},
	})
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
