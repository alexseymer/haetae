//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

func TestChatEnqueueClaimDeliver(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "chat-org")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelWeb, Kind: store.KindText, Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDueChatMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range claimed {
		if c.ID == m.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("expected claimed message")
	}
	if err := s.MarkDelivered(ctx, m.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	claimed2, err := s.ClaimDueChatMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claimed2 {
		if c.ID == m.ID {
			t.Fatal("delivered message claimed again")
		}
	}
}

func TestChatMarkFailedBackoffAndGiveUp(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "chat-fail")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, Kind: store.KindText, Body: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 1; i <= store.MaxDeliveryAttempts; i++ {
		if err := s.MarkFailed(ctx, m.ID, i, "boom", now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetChatMessage(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != store.MaxDeliveryAttempts {
		t.Fatalf("attempts=%d", got.Attempts)
	}
	if got.DeliveredAt != nil {
		t.Fatal("should keep undelivered row")
	}
	claimed, err := s.ClaimDueChatMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claimed {
		if c.ID == m.ID {
			t.Fatal("gave-up message should not be claimed")
		}
	}
}

func TestLinkCodeSingleUseExpiryRateLimitTakeover(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "link-org")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, org.ID, "admin@link.test", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.CreateUser(ctx, org.ID, "other@link.test", "hash", "member")
	if err != nil {
		t.Fatal(err)
	}

	code, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != store.LinkCodeLen {
		t.Fatalf("len=%d", len(code))
	}
	// Peek does not burn the code.
	orgID, userID, ch, err := s.ConsumeLinkCode(ctx, code)
	if err != nil || orgID != org.ID || userID != u.ID || ch != store.ChannelTelegram {
		t.Fatalf("peek: %v %v %v %v", orgID, userID, ch, err)
	}
	acc, err := s.LinkChatAccount(ctx, code, store.ChannelTelegram, "4242")
	if err != nil || acc.UserID != u.ID {
		t.Fatalf("link: %v %+v", err, acc)
	}
	if _, err := s.LinkChatAccount(ctx, code, store.ChannelTelegram, "4243"); !errors.Is(err, store.ErrLinkCodeInvalid) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := s.LinkChatAccount(ctx, "ZZZZZZZZ", store.ChannelTelegram, "1"); !errors.Is(err, store.ErrLinkCodeInvalid) {
		t.Fatalf("wrong: %v", err)
	}

	// Expiry: insert a code that is already expired via raw SQL.
	expired, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE chat_link_codes SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkChatAccount(ctx, expired, store.ChannelTelegram, "9"); !errors.Is(err, store.ErrLinkCodeInvalid) {
		t.Fatalf("expired: %v", err)
	}

	// Rate limit: create LinkCodeRateMax fresh codes.
	for i := 0; i < store.LinkCodeRateMax; i++ {
		if _, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelSlack); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelSlack); !errors.Is(err, store.ErrLinkCodeRateLimit) {
		t.Fatalf("rate limit: %v", err)
	}

	// Takeover refused: code stays valid when account insert fails.
	_, _ = s.Pool().Exec(ctx, `DELETE FROM chat_link_codes WHERE user_id = $1`, u2.ID)
	code2, err := s.CreateLinkCode(ctx, org.ID, u2.ID, store.ChannelTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkChatAccount(ctx, code2, store.ChannelTelegram, "111"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool().Exec(ctx, `DELETE FROM chat_link_codes WHERE user_id = $1`, u.ID)
	code3, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkChatAccount(ctx, code3, store.ChannelTelegram, "111"); !errors.Is(err, store.ErrAccountTaken) {
		t.Fatalf("takeover: %v", err)
	}
	// Code still usable on a free external id after takeover refusal.
	if _, err := s.LinkChatAccount(ctx, code3, store.ChannelTelegram, "222"); err != nil {
		t.Fatalf("code should remain after failed takeover: %v", err)
	}
}

func TestChatListMessagesTenantIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	orgA, err := s.CreateOrg(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	orgB, err := s.CreateOrg(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: orgA.ID, Channel: store.ChannelWeb, Kind: store.KindText, Body: "a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: orgB.ID, Channel: store.ChannelWeb, Kind: store.KindText, Body: "b",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ForOrg(orgA.ID).ListMessages(ctx, orgA.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.OrgID != orgA.ID {
			t.Fatalf("leaked org %s", m.OrgID)
		}
	}
	if len(list) != 1 {
		t.Fatalf("want 1, got %d", len(list))
	}
}

func TestChatAccountsCRUD(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "acc")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, org.ID, "u@acc.test", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateChatAccount(ctx, org.ID, u.ID, store.ChannelEmail, "u@acc.test")
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListChatAccounts(ctx, org.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	ver, err := s.ListVerifiedAccounts(ctx, org.ID, store.ChannelEmail)
	if err != nil || len(ver) != 1 {
		t.Fatalf("%v %v", ver, err)
	}
	if err := s.DeleteChatAccount(ctx, org.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChatAccount(ctx, org.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	_ = uuid.Nil
}
