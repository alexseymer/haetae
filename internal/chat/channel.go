// Package chat delivers outbound messages and bridges inbound channel traffic.
package chat

import (
	"context"

	"github.com/alexseymer/haetae/internal/store"
)

// Channel is one delivery and inbound bridge (web, Telegram, Slack, email, Signal).
type Channel interface {
	Name() string
	// Send delivers one outbound message. account is nil for org-wide web rows.
	Send(ctx context.Context, msg store.ChatMessage, account *store.ChatAccount) error
	// Run handles inbound traffic until ctx ends. May be a no-op.
	Run(ctx context.Context) error
}

// ActionHandler processes button/keyword actions and /link commands from channels.
type ActionHandler interface {
	HandleLink(ctx context.Context, channel, externalID, code string) (reply string, err error)
	HandleAction(ctx context.Context, channel, externalID, token string) (reply string, err error)
	HandleInboundText(ctx context.Context, channel, externalID, text string) (reply string, err error)
	HandleSignalKeyword(ctx context.Context, channel, externalID, keyword, messageRef string) (reply string, err error)
}

// LinkCodeSender is implemented by channels that cannot receive inbound
// messages (email): the server sends the link code to the user's own address
// and the user confirms it in the web UI.
type LinkCodeSender interface {
	SendLinkCode(ctx context.Context, to, code string) error
}
