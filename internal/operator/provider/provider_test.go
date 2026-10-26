package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const secretKey = "sk-SECRET-123456"

type captured struct {
	hdr  http.Header
	path string
	body map[string]any
}

func jsonEq(t *testing.T, got any, want string) {
	t.Helper()
	g, _ := json.Marshal(got)
	var a, b any
	_ = json.Unmarshal(g, &a)
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ga, _ := json.Marshal(a)
	wb, _ := json.Marshal(b)
	if string(ga) != string(wb) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", ga, wb)
	}
}

func server(t *testing.T, status int, reply string, cap *captured) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cap != nil {
			cap.hdr = r.Header.Clone()
			cap.path = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &cap.body)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func richRequest() Request {
	return Request{
		System: "be brief",
		Messages: []Message{
			{Role: RoleUser, Text: "why down?"},
			{Role: RoleAssistant, Text: "checking", ToolCalls: []ToolCall{
				{ID: "c1", Name: "list_hosts", Args: json.RawMessage(`{"a":1}`)},
				{ID: "c2", Name: "list_events", Args: json.RawMessage(`{}`)},
			}},
			{Role: RoleTool, ToolCallID: "c1", Text: "r1"},
			{Role: RoleTool, ToolCallID: "c2", Text: "r2"},
		},
		Tools:     []ToolSpec{{Name: "list_hosts", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 99999,
	}
}

func TestAnthropicWireFormat(t *testing.T) {
	var c captured
	if _, err := NewAnthropic(Config{BaseURL: "http://example.test", Model: "claude-x"}); err == nil {
		t.Fatal("http base must be rejected for anthropic")
	}
	// httptest is plain http: use the TLS variant for the real wire test.
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hdr = r.Header.Clone()
		c.path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c.body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"he"},{"type":"text","text":"llo"},{"type":"tool_use","id":"t1","name":"list_hosts","input":{"x":2}}],"usage":{"input_tokens":11,"output_tokens":7}}`)
	}))
	defer ts.Close()
	p, err := NewAnthropic(Config{BaseURL: ts.URL, APIKey: secretKey, Model: "claude-x", HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), richRequest())
	if err != nil {
		t.Fatal(err)
	}
	if c.path != "/v1/messages" || c.hdr.Get("x-api-key") != secretKey || c.hdr.Get("anthropic-version") != "2023-06-01" ||
		c.hdr.Get("content-type") != "application/json" || c.hdr.Get("User-Agent") != "haetae-operator" {
		t.Fatalf("bad request: %s %v", c.path, c.hdr)
	}
	jsonEq(t, c.body, `{"model":"claude-x","max_tokens":4096,"system":"be brief",
	 "messages":[
	  {"role":"user","content":[{"type":"text","text":"why down?"}]},
	  {"role":"assistant","content":[{"type":"text","text":"checking"},
	    {"type":"tool_use","id":"c1","name":"list_hosts","input":{"a":1}},
	    {"type":"tool_use","id":"c2","name":"list_events","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"r1"},{"type":"tool_result","tool_use_id":"c2","content":"r2"}]}],
	 "tools":[{"name":"list_hosts","description":"d","input_schema":{"type":"object"}}]}`)
	if resp.Text != "hello" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "t1" ||
		string(resp.ToolCalls[0].Args) != `{"x":2}` || resp.Usage != (Usage{11, 7}) {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestAnthropicDefaultMaxTokens(t *testing.T) {
	var c captured
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c.body)
		_, _ = io.WriteString(w, `{"content":[]}`)
	}))
	defer ts.Close()
	p, _ := NewAnthropic(Config{BaseURL: ts.URL, Model: "m", HTTPClient: ts.Client()})
	if _, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if c.body["max_tokens"].(float64) != 1024 {
		t.Fatalf("max_tokens = %v", c.body["max_tokens"])
	}
}

func TestOpenAIWireFormat(t *testing.T) {
	reply := `{"choices":[{"message":{"content":"ok","tool_calls":[{"id":"t1","type":"function","function":{"name":"list_hosts","arguments":"{\"x\":2}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`
	var c captured
	s := server(t, 200, reply, &c)
	p, err := New(Config{Kind: "local", BaseURL: s.URL + "/v1", Model: "llama", HTTPClient: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Local() || p.Name() != "local" {
		t.Fatalf("name/local = %s/%v", p.Name(), p.Local())
	}
	resp, err := p.Complete(context.Background(), richRequest())
	if err != nil {
		t.Fatal(err)
	}
	if c.path != "/v1/chat/completions" || c.hdr.Get("Authorization") != "" || c.hdr.Get("User-Agent") != "haetae-operator" {
		t.Fatalf("bad request: %s %v", c.path, c.hdr)
	}
	jsonEq(t, c.body, `{"model":"llama","max_tokens":4096,"messages":[
	  {"role":"system","content":"be brief"},
	  {"role":"user","content":"why down?"},
	  {"role":"assistant","content":"checking","tool_calls":[
	    {"id":"c1","type":"function","function":{"name":"list_hosts","arguments":"{\"a\":1}"}},
	    {"id":"c2","type":"function","function":{"name":"list_events","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c1","content":"r1"},
	  {"role":"tool","tool_call_id":"c2","content":"r2"}],
	 "tools":[{"type":"function","function":{"name":"list_hosts","description":"d","parameters":{"type":"object"}}}]}`)
	if resp.Text != "ok" || len(resp.ToolCalls) != 1 || string(resp.ToolCalls[0].Args) != `{"x":2}` || resp.Usage != (Usage{5, 3}) {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestOpenAIBearerOnlyWithKey(t *testing.T) {
	var c captured
	s := server(t, 200, `{"choices":[{"message":{"content":"x"}}]}`, &c)
	p, err := New(Config{Kind: "local", BaseURL: s.URL, Model: "m", APIKey: secretKey, HTTPClient: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if c.hdr.Get("Authorization") != "Bearer "+secretKey {
		t.Fatalf("auth = %q", c.hdr.Get("Authorization"))
	}
}

func TestOpenAIBadToolArguments(t *testing.T) {
	s := server(t, 200, `{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"n","arguments":"{nope"}}]}}]}`, nil)
	p, _ := New(Config{Kind: "local", BaseURL: s.URL, Model: "m", HTTPClient: s.Client()})
	_, err := p.Complete(context.Background(), Request{})
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v", err)
	}
}

// backends returns both real providers pointed at the handler.
func backends(t *testing.T, h http.Handler, to time.Duration) map[string]Provider {
	t.Helper()
	ts := httptest.NewTLSServer(h)
	t.Cleanup(ts.Close)
	a, err := NewAnthropic(Config{BaseURL: ts.URL, APIKey: secretKey, Model: "m", HTTPClient: ts.Client(), Timeout: to})
	if err != nil {
		t.Fatal(err)
	}
	o, err := New(Config{Kind: "openai", BaseURL: ts.URL, APIKey: secretKey, Model: "m", HTTPClient: ts.Client(), Timeout: to})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Provider{"anthropic": a, "openai": o}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"401", 401, "bad key " + secretKey, ErrAuth},
		{"403", 403, "", ErrAuth},
		{"429", 429, "slow down", ErrRateLimited},
		{"500", 500, "boom " + secretKey, ErrUnavailable},
		{"503", 503, "", ErrUnavailable},
		{"400", 400, "bad " + secretKey + strings.Repeat("x", 500), ErrBadResponse},
		{"404", 404, "", ErrBadResponse},
		{"redirect", 302, "", ErrBadResponse},
		{"not json", 200, "<html>", ErrBadResponse},
		{"empty", 200, "", ErrBadResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status == 302 {
					w.Header().Set("Location", "https://evil.example/x")
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			for name, p := range backends(t, h, 0) {
				_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
				if !errors.Is(err, tt.want) {
					t.Fatalf("%s: err = %v, want %v", name, err, tt.want)
				}
				if strings.Contains(err.Error(), secretKey) {
					t.Fatalf("%s: key leaked: %v", name, err)
				}
				if len(err.Error()) > 400 {
					t.Fatalf("%s: error too long (%d)", name, len(err.Error()))
				}
			}
		})
	}
}

func TestOversizeBody(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[],"choices":[],"pad":"`+strings.Repeat("a", 2<<20)+`"}`)
	})
	for name, p := range backends(t, h, 0) {
		_, err := p.Complete(context.Background(), Request{})
		if !errors.Is(err, ErrBadResponse) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	hit := false
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer other.Close()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	})
	for name, p := range backends(t, h, 0) {
		_, err := p.Complete(context.Background(), Request{})
		if !errors.Is(err, ErrBadResponse) || hit {
			t.Fatalf("%s: err = %v hit=%v", name, err, hit)
		}
	}
}

func TestTimeoutAndContext(t *testing.T) {
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	bs := backends(t, h, 50*time.Millisecond)
	t.Cleanup(func() { close(release) }) // runs before the server's Close
	for name, p := range bs {
		start := time.Now()
		_, err := p.Complete(context.Background(), Request{})
		if !errors.Is(err, ErrUnavailable) || time.Since(start) > 3*time.Second {
			t.Fatalf("%s: timeout err = %v", name, err)
		}
		if strings.Contains(err.Error(), secretKey) {
			t.Fatalf("key leaked: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Complete(ctx, Request{}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: ctx err = %v", name, err)
		}
	}
}

func TestNetworkErrorUnavailable(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	p, _ := NewAnthropic(Config{BaseURL: ts.URL, APIKey: secretKey, Model: "m", HTTPClient: ts.Client()})
	ts.Close()
	_, err := p.Complete(context.Background(), Request{})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), secretKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestScrubRemovesKeyAndClips(t *testing.T) {
	got := scrub("a "+secretKey+" "+strings.Repeat("y", 300), secretKey)
	if strings.Contains(got, secretKey) || len([]rune(got)) > 203 {
		t.Fatalf("scrub = %q", got)
	}
}

func TestLocalTruthTable(t *testing.T) {
	tests := []struct {
		kind, base string
		want       bool
	}{
		{"local", "http://localhost:11434/v1", true},
		{"local", "http://127.0.0.1:8080", true},
		{"local", "http://[::1]:8080", true},
		{"local", "http://10.1.2.3/v1", true},
		{"local", "http://192.168.0.5/v1", true},
		{"local", "http://172.16.0.1/v1", true},
		{"local", "https://[fc00::1]/v1", true},
		{"local", "https://[fd12:3456::1]/v1", true},
		{"local", "https://169.254.1.1/v1", false},
		{"local", "https://169.254.169.254/v1", false}, // cloud metadata
		{"local", "https://[fe80::1]/v1", false},
		{"local", "https://llm.internal.lan/v1", false}, // names are not resolved
		{"local", "https://8.8.8.8/v1", false},
		{"local", "https://api.example.com/v1", false},
		{"anthropic", "", false},
		{"openai", "", false},
		{"openai", "https://10.0.0.1/v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.kind+" "+tt.base, func(t *testing.T) {
			p, err := New(Config{Kind: tt.kind, BaseURL: tt.base, Model: "m", APIKey: "k"})
			if err != nil {
				t.Fatal(err)
			}
			if p.Local() != tt.want {
				t.Fatalf("Local() = %v, want %v", p.Local(), tt.want)
			}
		})
	}
}

func TestLinkLocalIsNotLocalOverHTTP(t *testing.T) {
	for _, base := range []string{"http://169.254.169.254/v1", "http://[fe80::1]/v1"} {
		if _, err := New(Config{Kind: "local", BaseURL: base, Model: "m", APIKey: "k"}); err == nil {
			t.Fatalf("%s: plain http to a non-local host was accepted", base)
		}
	}
}

func TestFactoryValidation(t *testing.T) {
	for _, k := range []string{"", "off", "OFF"} {
		if p, err := New(Config{Kind: k}); p != nil || err != nil {
			t.Fatalf("kind %q: %v %v", k, p, err)
		}
	}
	bad := []Config{
		{Kind: "bogus", Model: "m"},
		{Kind: "anthropic"},
		{Kind: "openai"},
		{Kind: "local", BaseURL: "http://localhost:1"},
		{Kind: "local", Model: "m"},
		{Kind: "local", Model: "m", BaseURL: "ftp://localhost"},
		{Kind: "local", Model: "m", BaseURL: "http://"},
		{Kind: "local", Model: "m", BaseURL: "localhost:11434"},
		{Kind: "local", Model: "m", BaseURL: "http://example.com/v1"},
		{Kind: "anthropic", Model: "m", BaseURL: "http://localhost:1"},
		{Kind: "openai", Model: "m", BaseURL: "http://api.openai.com/v1"},
		{Kind: "openai", Model: "m", BaseURL: "://bad"},
	}
	for i, c := range bad {
		if p, err := New(c); err == nil || p != nil {
			t.Fatalf("case %d (%+v): expected error", i, c)
		}
	}
	if p, err := New(Config{Kind: "anthropic", Model: "m", APIKey: "k"}); err != nil || p.Name() != "anthropic" || p.Local() {
		t.Fatalf("anthropic default: %v %v", p, err)
	}
	if p, err := New(Config{Kind: "openai", Model: "m", APIKey: "k"}); err != nil || p.Name() != "openai" {
		t.Fatalf("openai default: %v %v", p, err)
	}
}

// Conformance: the same scripted conversation behaves the same on every backend.
func TestConformance(t *testing.T) {
	anReplies := []string{
		`{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"content":[{"type":"tool_use","id":"t1","name":"list_hosts","input":{"k":"v"}}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	oaReplies := []string{
		`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"list_hosts","arguments":"{\"k\":\"v\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	scripted := func(replies []string) http.Handler {
		n := 0
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, replies[n])
			n++
		})
	}
	anP := backends(t, scripted(anReplies), 0)["anthropic"]
	oaP := backends(t, scripted(oaReplies), 0)["openai"]
	fake := &Fake{Responses: []Response{
		{Text: "hi", Usage: Usage{1, 1}},
		{ToolCalls: []ToolCall{{ID: "t1", Name: "list_hosts", Args: json.RawMessage(`{"k":"v"}`)}}, Usage: Usage{1, 1}},
		{Text: "done", Usage: Usage{1, 1}},
	}}
	for name, p := range map[string]Provider{"anthropic": anP, "openai": oaP, "fake": fake} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tools := []ToolSpec{{Name: "list_hosts", Schema: json.RawMessage(`{"type":"object"}`)}}
			msgs := []Message{{Role: RoleUser, Text: "hello"}}
			r, err := p.Complete(ctx, Request{System: "s", Messages: msgs, Tools: tools})
			if err != nil || r.Text != "hi" || len(r.ToolCalls) != 0 || r.Usage != (Usage{1, 1}) {
				t.Fatalf("step1: %+v %v", r, err)
			}
			msgs = append(msgs, Message{Role: RoleAssistant, Text: r.Text}, Message{Role: RoleUser, Text: "list"})
			r, err = p.Complete(ctx, Request{System: "s", Messages: msgs, Tools: tools})
			if err != nil || r.Text != "" || len(r.ToolCalls) != 1 || r.ToolCalls[0].ID != "t1" ||
				r.ToolCalls[0].Name != "list_hosts" || string(r.ToolCalls[0].Args) != `{"k":"v"}` {
				t.Fatalf("step2: %+v %v", r, err)
			}
			msgs = append(msgs, Message{Role: RoleAssistant, ToolCalls: r.ToolCalls}, Message{Role: RoleTool, ToolCallID: "t1", Text: "[]"})
			r, err = p.Complete(ctx, Request{System: "s", Messages: msgs, Tools: tools})
			if err != nil || r.Text != "done" || len(r.ToolCalls) != 0 {
				t.Fatalf("step3: %+v %v", r, err)
			}
		})
	}
}
