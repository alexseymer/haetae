//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

func TestConsumeActionRefRolledBackWhenEffectFails(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "act-tx")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, Kind: store.KindIncident, Body: "x",
		Actions: []store.ChatAction{{ID: "later", Label: "Later"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.CreateActionRefs(ctx, org.ID, msg.ID, []string{"later"}, time.Now().Add(time.Hour))
	if err != nil || refs["later"] == "" {
		t.Fatal(err)
	}

	// Effect fails: inbound insert references a non-existent account_id FK.
	badAcc := uuid.New()
	err = s.ApplyActionInbound(ctx, msg.ID, "later", store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, AccountID: &badAcc,
		Kind: store.KindAction, Body: "later",
	})
	if err == nil {
		t.Fatal("expected effect failure")
	}

	// Ref must still be usable.
	mid, action, err := s.ResolveActionRef(ctx, refs["later"])
	if err != nil || mid != msg.ID || action != "later" {
		t.Fatalf("ref burned after failed effect: %v %s %s", err, mid, action)
	}

	// Successful consume+effect.
	u, err := s.CreateUser(ctx, org.ID, "a@act-tx.test", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	acc, err := s.CreateChatAccount(ctx, org.ID, u.ID, store.ChannelTelegram, "1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyActionInbound(ctx, msg.ID, "later", store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, AccountID: &acc.ID,
		Kind: store.KindAction, Body: "later",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveActionRef(ctx, refs["later"]); err == nil {
		t.Fatal("ref should be consumed after success")
	}
}

func TestCreateActionRefsOmitsUsedActions(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "ref-skip")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, Kind: store.KindIncident, Body: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	refs1, err := s.CreateActionRefs(ctx, org.ID, msg.ID, []string{"later", "ack"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeActionRef(ctx, msg.ID, "later"); err != nil {
		t.Fatal(err)
	}
	refs2, err := s.CreateActionRefs(ctx, org.ID, msg.ID, []string{"later", "ack"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refs2["later"]; ok {
		t.Fatal("used action later must be omitted (dead ref)")
	}
	if refs2["ack"] == "" {
		t.Fatal("unused ack must still get a working ref")
	}
	if refs2["ack"] == refs1["ack"] {
		// May or may not rotate; either is fine as long as resolve works.
	}
	mid, action, err := s.ResolveActionRef(ctx, refs2["ack"])
	if err != nil || mid != msg.ID || action != "ack" {
		t.Fatalf("redelivery ack ref broken: %v", err)
	}
}
