//go:build integration

package chat_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/telegram"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

type countingSender struct {
	n atomic.Int32
}

func (c *countingSender) Name() string              { return store.ChannelTelegram }
func (c *countingSender) Run(context.Context) error { return nil }
func (c *countingSender) Send(context.Context, store.ChatMessage, *store.ChatAccount) error {
	c.n.Add(1)
	return nil
}

func TestSendOKMarkDeliveredFailNoResend(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _, admin := seedOrg(t, s, "dup-org", "a@dup.test")
	sender := &countingSender{}
	svc := chat.New(chat.Config{
		Store: s, Secret: []byte("0123456789abcdef0123456789abcdef"),
		Channels: []chat.Channel{web.Channel{}, sender}, PollEvery: time.Hour,
	})
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "99")

	msg, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, Kind: store.KindText, Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	accs, _ := s.ListVerifiedAccounts(ctx, org.ID, store.ChannelTelegram)
	if len(accs) == 0 {
		t.Fatal("no account")
	}
	id := accs[0].ID
	_, _ = s.Pool().Exec(ctx, `UPDATE chat_messages SET account_id = $1 WHERE id = $2`, id, msg.ID)

	svc.DeliverOnceForTest(ctx)
	if sender.n.Load() != 1 {
		t.Fatalf("sends=%d", sender.n.Load())
	}
	svc.RememberDeliveredForTest(msg.ID)
	_, _ = s.Pool().Exec(ctx, `
		UPDATE chat_messages SET delivered_at = NULL, next_attempt_at = now(), attempts = 0 WHERE id = $1`, msg.ID)
	svc.DeliverOnceForTest(ctx)
	svc.DeliverOnceForTest(ctx)
	if sender.n.Load() != 1 {
		t.Fatalf("resent after MarkDelivered failure: sends=%d", sender.n.Load())
	}
}

func TestUnlinkedInboundSilentExceptLinkAndStart(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	svc := chat.New(chat.Config{
		Store: s, Secret: []byte("0123456789abcdef0123456789abcdef"),
		Channels: []chat.Channel{web.Channel{}},
	})
	reply, err := svc.HandleInboundText(ctx, store.ChannelTelegram, "1", "hello there")
	if err != nil || reply != "" {
		t.Fatalf("want silent, got %q %v", reply, err)
	}
	reply, err = svc.HandleInboundText(ctx, store.ChannelTelegram, "1", "/start")
	if err != nil || reply == "" {
		t.Fatalf("want /start reply, got %q %v", reply, err)
	}
}

func TestTelegramCallbackDataOpaqueAndWithin64(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, host, admin := seedOrg(t, s, "opaque-org", "a@opaque.test")

	var sawData atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "sendMessage") {
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if text, _ := payload["text"].(string); len(text) > 4096 {
				http.Error(w, "too long", 400)
				return
			}
			if rm, ok := payload["reply_markup"].(map[string]any); ok {
				if rows, ok := rm["inline_keyboard"].([]any); ok {
					for _, row := range rows {
						arr, _ := row.([]any)
						for _, cell := range arr {
							btn, _ := cell.(map[string]any)
							data, _ := btn["callback_data"].(string)
							if len(data) > 64 {
								http.Error(w, "callback_data too long", 400)
								return
							}
							sawData.Store(data)
						}
					}
				}
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	tg := &telegram.Channel{Token: "t", BaseURL: srv.URL, Client: srv.Client()}
	svc := chat.New(chat.Config{
		Store: s, Secret: []byte("0123456789abcdef0123456789abcdef"),
		Channels:  []chat.Channel{web.Channel{}, tg},
		PollEvery: time.Hour,
	})
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "7")
	inc, err := s.InsertIncidentForTest(ctx, store.Incident{
		OrgID: org.ID, HostID: host.ID, Title: "t", Severity: store.SeverityHigh, State: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.IncidentChanged(ctx, org.ID, inc, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10 && sawData.Load() == nil; i++ {
		svc.DeliverOnceForTest(ctx)
		time.Sleep(30 * time.Millisecond)
	}
	data, _ := sawData.Load().(string)
	if data == "" || !strings.HasPrefix(data, "a:") || len(data) > 64 {
		t.Fatalf("callback_data=%q", data)
	}
}
