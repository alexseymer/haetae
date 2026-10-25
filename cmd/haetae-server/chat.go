package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/email"
	"github.com/alexseymer/haetae/internal/chat/signal"
	"github.com/alexseymer/haetae/internal/chat/slack"
	"github.com/alexseymer/haetae/internal/chat/telegram"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
)

// startChat builds configured chat channels from the environment and runs the
// delivery / inbound loops until ctx ends. Channels without env config are
// omitted and make no network calls. The returned service implements
// alerts.Notifier and briefing.Enqueuer.
func startChat(ctx context.Context, st *store.Store, dataDir string, log *slog.Logger) *chat.Service {
	secret, err := chat.LoadChatSecret(dataDir)
	if err != nil {
		log.Warn("chat secret", "err", err)
		secret = []byte("haetae-chat-dev-secret-change-me!!")
	}
	publicURL := envOr("HAETAE_PUBLIC_URL", envOr("HAETAE_WEB_URL", ""))

	channels, attach := buildChatChannels(publicURL)
	svc := chat.New(chat.Config{
		Store:     st,
		Secret:    secret,
		PublicURL: publicURL,
		Log:       log.With("component", "chat"),
		Channels:  channels,
	})
	attach(svc)

	go func() {
		if err := svc.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("chat service stopped", "err", err)
		}
	}()
	log.Info("chat service started", "channels", svc.ChannelNames())
	return svc
}

func buildChatChannels(publicURL string) (channels []chat.Channel, attach func(*chat.Service)) {
	channels = []chat.Channel{web.Channel{}}
	var (
		tg  *telegram.Channel
		sl  *slack.Channel
		sig *signal.Channel
	)

	if tok := os.Getenv("HAETAE_TELEGRAM_TOKEN"); tok != "" {
		tg = &telegram.Channel{
			Token:   tok,
			BaseURL: os.Getenv("HAETAE_TELEGRAM_BASE_URL"), // empty → api.telegram.org; tests point at a fake
		}
		channels = append(channels, tg)
	}
	appTok := os.Getenv("HAETAE_SLACK_APP_TOKEN")
	botTok := os.Getenv("HAETAE_SLACK_BOT_TOKEN")
	if appTok != "" && botTok != "" {
		sl = &slack.Channel{AppToken: appTok, BotToken: botTok}
		channels = append(channels, sl)
	}
	if addr := os.Getenv("HAETAE_SMTP_ADDR"); addr != "" && os.Getenv("HAETAE_SMTP_FROM") != "" {
		channels = append(channels, email.New(email.Config{
			Addr:      addr,
			User:      os.Getenv("HAETAE_SMTP_USER"),
			Pass:      os.Getenv("HAETAE_SMTP_PASS"),
			From:      os.Getenv("HAETAE_SMTP_FROM"),
			PublicURL: publicURL,
			Insecure:  os.Getenv("HAETAE_SMTP_INSECURE") == "1",
		}))
	}
	if u := os.Getenv("HAETAE_SIGNAL_URL"); u != "" {
		sig = &signal.Channel{URL: u}
		channels = append(channels, sig)
	}

	attach = func(svc *chat.Service) {
		if tg != nil {
			tg.Handler = svc
		}
		if sl != nil {
			sl.Handler = svc
		}
		if sig != nil {
			sig.Handler = svc
		}
	}
	return channels, attach
}
