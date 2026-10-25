// Package web is the in-app chat channel: the row is the message.
package web

import (
	"context"

	"github.com/alexseymer/haetae/internal/store"
)

// Channel implements chat.Channel for the web UI.
type Channel struct{}

// Name returns store.ChannelWeb.
func (Channel) Name() string { return store.ChannelWeb }

// Send is a no-op; delivery marks the row delivered.
func (Channel) Send(context.Context, store.ChatMessage, *store.ChatAccount) error {
	return nil
}

// Run is a no-op; inbound web chat goes through the HTTP API.
func (Channel) Run(context.Context) error { return nil }
