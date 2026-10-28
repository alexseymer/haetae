package api

import (
	"context"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeChatChannel struct{ name string }

func (f fakeChatChannel) Name() string { return f.name }
func (f fakeChatChannel) Send(context.Context, store.ChatMessage, *store.ChatAccount) error {
	return nil
}
func (f fakeChatChannel) Run(context.Context) error { return nil }

func TestChannelEnabledUsesConfiguredChannels(t *testing.T) {
	secret := []byte("unit-test-chat-secret-0123456789ab")
	svc := chat.New(chat.Config{
		Secret: secret,
		Channels: []chat.Channel{
			web.Channel{},
			fakeChatChannel{store.ChannelTelegram},
		},
	})
	a := &API{cfg: Config{Chat: svc}}
	if !a.channelEnabled(store.ChannelWeb) {
		t.Fatal("web should be enabled")
	}
	if !a.channelEnabled(store.ChannelTelegram) {
		t.Fatal("telegram should be enabled when configured")
	}
	if a.channelEnabled(store.ChannelSlack) {
		t.Fatal("slack should be disabled when not configured")
	}
	a2 := &API{cfg: Config{}}
	if !a2.channelEnabled(store.ChannelWeb) {
		t.Fatal("web enabled without chat service")
	}
	if a2.channelEnabled(store.ChannelTelegram) {
		t.Fatal("telegram disabled without chat service")
	}
}

func TestLinkInstructionsContainCodeNotSecrets(t *testing.T) {
	code := "ABCD2345"
	for _, ch := range []string{store.ChannelTelegram, store.ChannelSlack, store.ChannelSignal} {
		s := linkInstructions(ch, code)
		if s == "" || !strings.Contains(s, code) {
			t.Fatalf("%s instructions missing code: %q", ch, s)
		}
	}
}

func TestLinkInstructionsPerChannel(t *testing.T) {
	code := "ABCD2345"
	sl := linkInstructions("slack", code)
	if !strings.Contains(sl, "link "+code) || strings.Contains(sl, "/link") || !strings.Contains(sl, "direct message") {
		t.Fatalf("slack: %q", sl)
	}
	if em := linkInstructions("email", code); strings.Contains(em, "/link") || strings.Contains(em, code) {
		t.Fatalf("email: %q", em)
	}
	for _, ch := range []string{"telegram", "signal"} {
		if s := linkInstructions(ch, code); !strings.Contains(s, "/link "+code) {
			t.Fatalf("%s: %q", ch, s)
		}
	}
}

func TestChatMessageJSONIncludesMeta(t *testing.T) {
	m := store.ChatMessage{Kind: store.KindIncident, Meta: &store.ChatMeta{Severity: "critical", Title: "t", State: "open", Host: "h"}}
	got, ok := chatMessageJSON(m)["meta"].(*store.ChatMeta)
	if !ok || got.Title != "t" || got.Host != "h" {
		t.Fatalf("meta: %#v", got)
	}
	if _, has := chatMessageJSON(store.ChatMessage{Kind: store.KindText})["meta"]; has {
		t.Fatal("meta must be absent for non-incident messages")
	}
}
