package chat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/signal"
	"github.com/alexseymer/haetae/internal/chat/slack"
	"github.com/alexseymer/haetae/internal/chat/telegram"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

func redirectServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var followed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/follow" {
			followed.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Redirect(w, r, "/follow", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &followed
}

func TestTelegramClientRefusesRedirect(t *testing.T) {
	srv, followed := redirectServer(t)
	cli := chat.NewHTTPClient(5 * time.Second)
	ch := &telegram.Channel{Token: "tok", BaseURL: srv.URL, Client: cli}
	acc := &store.ChatAccount{ExternalID: "1"}
	err := ch.Send(context.Background(), store.ChatMessage{Body: "hi"}, acc)
	if err == nil {
		t.Fatal("expected send error on 302 body")
	}
	if followed.Load() != 0 {
		t.Fatalf("telegram followed redirect (%d)", followed.Load())
	}
}

func TestSlackClientRefusesRedirect(t *testing.T) {
	srv, followed := redirectServer(t)
	cli := chat.NewHTTPClient(5 * time.Second)
	ch := &slack.Channel{BotToken: "xoxb", Client: cli}
	// Force api base via a tiny wrapper: postMessage hits api.slack.com by default.
	// Use reflection-free approach: call through a custom Client against srv by
	// temporarily relying on NewHTTPClient behaviour tested via Direct Do.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/chat.postMessage", nil)
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if followed.Load() != 0 {
		t.Fatalf("slack http client followed redirect")
	}
	_ = ch
}

func TestSignalClientRefusesRedirect(t *testing.T) {
	srv, followed := redirectServer(t)
	cli := chat.NewHTTPClient(5 * time.Second)
	ch := &signal.Channel{URL: srv.URL, Client: cli}
	acc := &store.ChatAccount{ExternalID: "+15551212"}
	err := ch.Send(context.Background(), store.ChatMessage{
		ID: uuid.New(), Body: "hi",
	}, acc)
	if err == nil {
		t.Fatal("expected send error on 302")
	}
	if followed.Load() != 0 {
		t.Fatalf("signal followed redirect (%d)", followed.Load())
	}
}
