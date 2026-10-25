//go:build integration

package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestSendOKRestartNoResendViaSentAt(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _, admin := seedOrg(t, s, "restart-org", "a@restart.test")
	sender := &countingSender{}
	svc := chat.New(chat.Config{
		Store: s, Secret: []byte("0123456789abcdef0123456789abcdef"),
		Channels: []chat.Channel{web.Channel{}, sender}, PollEvery: time.Hour,
	})
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "42")

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
	_, _ = s.Pool().Exec(ctx, `UPDATE chat_messages SET account_id = $1 WHERE id = $2`, accs[0].ID, msg.ID)

	svc.DeliverOnceForTest(ctx)
	if sender.n.Load() != 1 {
		t.Fatalf("sends=%d", sender.n.Load())
	}

	// Simulate MarkDelivered never sticking, but sent_at persisted.
	_, _ = s.Pool().Exec(ctx, `
		UPDATE chat_messages
		SET delivered_at = NULL, next_attempt_at = now(), attempts = 0
		WHERE id = $1`, msg.ID)
	var sentAt *time.Time
	if err := s.Pool().QueryRow(ctx, `SELECT sent_at FROM chat_messages WHERE id=$1`, msg.ID).Scan(&sentAt); err != nil || sentAt == nil {
		t.Fatalf("sent_at not persisted: %v %v", sentAt, err)
	}

	// New Service instance (process restart) over the same DB.
	svc2 := chat.New(chat.Config{
		Store: s, Secret: []byte("0123456789abcdef0123456789abcdef"),
		Channels: []chat.Channel{web.Channel{}, sender}, PollEvery: time.Hour,
	})
	svc2.DeliverOnceForTest(ctx)
	svc2.DeliverOnceForTest(ctx)
	if sender.n.Load() != 1 {
		t.Fatalf("resent after restart: sends=%d", sender.n.Load())
	}
}
