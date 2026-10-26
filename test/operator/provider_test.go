//go:build e2e_operator

package operator_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// oaMsg is one message of an OpenAI-format chat completion request.
type oaMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
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

// recorded is one request the provider received.
type recorded struct {
	At       time.Time
	Raw      string
	System   string
	Messages []oaMsg
	Tag      string // scenario tag taken from the first user message: "[tag] ..."
}

// toolResults returns the parsed tool results of the request keyed by tool
// name (the last result of a tool wins), and the raw text of each.
func (r *recorded) toolResults() map[string]map[string]any {
	names := map[string]string{}
	for _, m := range r.Messages {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Function.Name
		}
	}
	out := map[string]map[string]any{}
	for _, m := range r.Messages {
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

// evidenceID returns the evidence_id of the named tool's result ("" if none).
func (r *recorded) evidenceID(tool string) string {
	id, _ := r.toolResults()[tool]["evidence_id"].(string)
	return id
}

// containerIDWithPrefix finds, in the list_containers result, the container
// id that starts with the first 12 chars of dockerID.
func (r *recorded) containerIDWithPrefix(dockerID string) string {
	data, _ := r.toolResults()["list_containers"]["data"].(map[string]any)
	cs, _ := data["containers"].([]any)
	for _, c := range cs {
		m, _ := c.(map[string]any)
		id, _ := m["container_id"].(string)
		if id != "" && strings.HasPrefix(dockerID, id[:min(12, len(id))]) {
			return id
		}
	}
	return ""
}

type call struct {
	Name string
	Args any
}

// reply is what a script answers: tool calls, or final text.
type reply struct {
	Text  string
	Calls []call
}

type scriptFn func(r *recorded) reply

const (
	modeOK   = "ok"
	modeHang = "hang"
	mode500  = "500"
)

// scriptedProvider is an OpenAI-compatible /v1/chat/completions server.
type scriptedProvider struct {
	srv *httptest.Server

	mu      sync.Mutex
	reqs    []*recorded
	scripts map[string]scriptFn
	mode    string
	release chan struct{} // closed to free hanging handlers
}

var tagRe = regexp.MustCompile(`^\[([a-z0-9_-]+)\]`)

func startScriptedProvider(t *testing.T) *scriptedProvider {
	p := &scriptedProvider{scripts: map[string]scriptFn{}, mode: modeOK, release: make(chan struct{})}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(func() {
		p.setMode(modeOK)
		p.srv.CloseClientConnections()
		p.srv.Close()
	})
	return p
}

func (p *scriptedProvider) URL() string { return p.srv.URL }

func (p *scriptedProvider) setScript(tag string, fn scriptFn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scripts[tag] = fn
}

// setMode switches behaviour; leaving hang mode frees blocked handlers.
func (p *scriptedProvider) setMode(m string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == modeHang && m != modeHang {
		close(p.release)
		p.release = make(chan struct{})
	}
	p.mode = m
}

func (p *scriptedProvider) requests() []*recorded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*recorded(nil), p.reqs...)
}

func (p *scriptedProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

func (p *scriptedProvider) byTag(tag string) []*recorded {
	var out []*recorded
	for _, r := range p.requests() {
		if r.Tag == tag {
			out = append(out, r)
		}
	}
	return out
}

func (p *scriptedProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []oaMsg `json:"messages"`
	}
	_ = json.Unmarshal(raw, &body)
	rec := &recorded{At: time.Now(), Raw: string(raw), Messages: body.Messages}
	for _, m := range body.Messages {
		if m.Role == "system" && rec.System == "" {
			rec.System = m.text()
		}
	}
	for _, m := range body.Messages {
		if m.Role == "user" {
			if g := tagRe.FindStringSubmatch(m.text()); g != nil {
				rec.Tag = g[1]
			}
			break
		}
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, rec)
	mode, release, fn := p.mode, p.release, p.scripts[rec.Tag]
	p.mu.Unlock()

	switch mode {
	case modeHang:
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return
	case mode500:
		http.Error(w, `{"error":"scripted failure"}`, http.StatusInternalServerError)
		return
	}
	rep := reply{Text: `{"statements":[],"actions":[]}`} // incident reports, judge, briefing: nothing to say
	if fn != nil {
		rep = fn(rec)
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
	})
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// window is a get_logs / events time window around now.
func window() (string, string) {
	now := time.Now().UTC()
	return now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
}
