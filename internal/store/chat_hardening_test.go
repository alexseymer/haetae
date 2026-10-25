//go:build integration

package store_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

func TestLinkChatAccountChannelMismatchKeepsCode(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "link-org")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, org.ID, "a@link.test", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkChatAccount(ctx, code, store.ChannelSlack, "U1"); err == nil {
		t.Fatal("expected channel mismatch failure")
	}
	// Code still valid for the correct channel.
	if _, err := s.LinkChatAccount(ctx, code, store.ChannelTelegram, "4242"); err != nil {
		t.Fatalf("code should still work: %v", err)
	}
}

func TestCreateLinkCodeRateLimitAtomic(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "rate-org")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, org.ID, "a@rate.test", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	var ok, limited atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateLinkCode(ctx, org.ID, u.ID, store.ChannelTelegram)
			if err == nil {
				ok.Add(1)
			} else if err == store.ErrLinkCodeRateLimit {
				limited.Add(1)
			} else {
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != int32(store.LinkCodeRateMax) {
		t.Fatalf("ok=%d limited=%d want ok=%d", ok.Load(), limited.Load(), store.LinkCodeRateMax)
	}
}

func TestActionRefTelegramSized(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "ref-org")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, Kind: store.KindIncident, Body: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.CreateActionRefs(ctx, org.ID, msg.ID, []string{"later", "mute30d", "ack"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for a, ref := range refs {
		payload := store.ActionRefPrefix + ref
		if len(payload) > 64 {
			t.Fatalf("%s payload %d > 64", a, len(payload))
		}
		if len(payload) > 24 {
			t.Fatalf("%s payload %d > 24 (want ≤24)", a, len(payload))
		}
		mid, action, err := s.ResolveActionRef(ctx, ref)
		if err != nil || mid != msg.ID || action != a {
			t.Fatalf("resolve %s: %v %s %s", a, err, mid, action)
		}
	}
}

func TestMuteDedupeExtendsUntil(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "mute-org")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "h", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "ser", nil })
	if err != nil {
		t.Fatal(err)
	}
	until1 := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	until2 := until1.Add(48 * time.Hour)
	m1, err := s.CreateMute(ctx, store.Mute{OrgID: org.ID, HostID: &host.ID, Rule: "oom_kill", Until: until1})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.CreateMute(ctx, store.Mute{OrgID: org.ID, HostID: &host.ID, Rule: "oom_kill", Until: until2})
	if err != nil {
		t.Fatal(err)
	}
	if m1.ID != m2.ID {
		t.Fatalf("expected same mute row, got %s vs %s", m1.ID, m2.ID)
	}
	if !m2.Until.Equal(until2) {
		t.Fatalf("until not extended: %v", m2.Until)
	}
	list, err := s.ListMutes(ctx, org.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("want 1 mute, got %d %v", len(list), err)
	}
}
