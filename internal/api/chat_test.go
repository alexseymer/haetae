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
	for _, ch := range []string{store.ChannelTelegram, store.ChannelSlack, store.ChannelEmail, store.ChannelSignal} {
		s := linkInstructions(ch, code)
		if s == "" || !strings.Contains(s, code) {
			t.Fatalf("%s instructions missing code: %q", ch, s)
		}
	}
}
