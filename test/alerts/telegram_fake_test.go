//go:build e2e_alerts

package alerts_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// fakeTelegram is a minimal Bot API that records sendMessage calls and serves
// queued getUpdates (messages and callback queries) to the real telegram bridge.
type fakeTelegram struct {
	srv *httptest.Server

	mu       sync.Mutex
	updates  []map[string]any
	waiters  []chan struct{}
	nextID   int64
	messages []tgSent
}

type tgSent struct {
	At       time.Time
	ChatID   int64
	Text     string
	Keyboard []tgButton
	Raw      map[string]any
}

type tgButton struct {
	Text string
	Data string
}

func startFakeTelegram(t testingT) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// testingT is the subset of *testing.T we need (avoids importing testing in helpers only).
type testingT interface {
	Helper()
	Cleanup(func())
	Fatalf(format string, args ...any)
}

func (f *fakeTelegram) URL() string { return f.srv.URL }

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	path := r.URL.Path
	switch {
	case strings.Contains(path, "sendMessage"):
		f.recordSend(body)
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	case strings.Contains(path, "getUpdates"):
		f.serveUpdates(w, body)
	case strings.Contains(path, "answerCallbackQuery"):
		writeJSON(w, map[string]any{"ok": true, "result": true})
	default:
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (f *fakeTelegram) recordSend(body []byte) {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	chatID, _ := asInt64(payload["chat_id"])
	text, _ := payload["text"].(string)
	var keys []tgButton
	if rm, ok := payload["reply_markup"].(map[string]any); ok {
		if rows, ok := rm["inline_keyboard"].([]any); ok {
			for _, row := range rows {
				arr, _ := row.([]any)
				for _, cell := range arr {
					btn, _ := cell.(map[string]any)
					keys = append(keys, tgButton{
						Text: str(btn["text"]),
						Data: str(btn["callback_data"]),
					})
				}
			}
		}
	}
	f.mu.Lock()
	f.messages = append(f.messages, tgSent{
		At: time.Now(), ChatID: chatID, Text: text, Keyboard: keys, Raw: payload,
	})
	f.mu.Unlock()
}

func (f *fakeTelegram) serveUpdates(w http.ResponseWriter, body []byte) {
	var req struct {
		Offset  int `json:"offset"`
		Timeout int `json:"timeout"`
	}
	_ = json.Unmarshal(body, &req)
	deadline := time.Now().Add(2 * time.Second)
	if req.Timeout > 0 && req.Timeout < 5 {
		deadline = time.Now().Add(time.Duration(req.Timeout) * time.Second)
	}

	for {
		f.mu.Lock()
		if len(f.updates) > 0 {
			batch := f.updates
			f.updates = nil
			f.mu.Unlock()
			writeJSON(w, map[string]any{"ok": true, "result": batch})
			return
		}
		ch := make(chan struct{}, 1)
		f.waiters = append(f.waiters, ch)
		f.mu.Unlock()

		if time.Now().After(deadline) {
			writeJSON(w, map[string]any{"ok": true, "result": []any{}})
			return
		}
		select {
		case <-ch:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *fakeTelegram) push(update map[string]any) {
	f.mu.Lock()
	id := atomic.AddInt64(&f.nextID, 1)
	update["update_id"] = id
	f.updates = append(f.updates, update)
	waiters := f.waiters
	f.waiters = nil
	f.mu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeTelegram) PushMessage(fromID int64, text string) {
	f.push(map[string]any{
		"message": map[string]any{
			"text": text,
			"chat": map[string]any{"id": fromID},
			"from": map[string]any{"id": fromID},
		},
	})
}

func (f *fakeTelegram) PushCallback(fromID int64, data string) {
	f.push(map[string]any{
		"callback_query": map[string]any{
			"id":   strconv.FormatInt(fromID, 10) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10),
			"data": data,
			"from": map[string]any{"id": fromID},
			"message": map[string]any{
				"chat": map[string]any{"id": fromID},
			},
		},
	})
}

func (f *fakeTelegram) Messages() []tgSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tgSent, len(f.messages))
	copy(out, f.messages)
	return out
}

func (f *fakeTelegram) CountSince(chatID int64, since time.Time, pred func(tgSent) bool) int {
	n := 0
	for _, m := range f.Messages() {
		if m.ChatID != chatID || m.At.Before(since) {
			continue
		}
		if pred == nil || pred(m) {
			n++
		}
	}
	return n
}

func (f *fakeTelegram) FindMuteToken(chatID int64, since time.Time) string {
	// Prefer the most recent Mute button (e.g. host_offline over an earlier crash_loop).
	token := ""
	for _, m := range f.Messages() {
		if m.ChatID != chatID || m.At.Before(since) {
			continue
		}
		for _, b := range m.Keyboard {
			if strings.Contains(strings.ToLower(b.Text), "mute") {
				token = b.Data
			}
		}
	}
	return token
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func asInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
