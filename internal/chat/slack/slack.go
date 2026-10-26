// Package slack bridges Slack via Socket Mode (apps.connections.open + WebSocket).
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/store"
)

// Handler receives inbound Slack traffic.
type Handler interface {
	HandleInboundText(ctx context.Context, channel, externalID, text string) (reply string, err error)
	HandleAction(ctx context.Context, channel, externalID, token string) (reply string, err error)
}

// Channel uses Slack Socket Mode.
type Channel struct {
	AppToken string // xapp-...
	BotToken string // xoxb-...
	APIURL   string // default https://slack.com/api; override in tests
	// ResponseURLHosts is the allowlist for interactive response_url hosts
	// (default hooks.slack.com). Tests may set a fake host.
	ResponseURLHosts []string
	WSDial           func(ctx context.Context, url string) (*websocket.Conn, error)
	Client           *http.Client
	Handler          Handler
}

// Name returns store.ChannelSlack.
func (c *Channel) Name() string { return store.ChannelSlack }

func (c *Channel) apiBase() string {
	if c.APIURL != "" {
		return strings.TrimRight(c.APIURL, "/")
	}
	return "https://slack.com/api"
}

func (c *Channel) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return chat.NewHTTPClient(30 * time.Second)
}

func (c *Channel) responseHosts() []string {
	if len(c.ResponseURLHosts) > 0 {
		return c.ResponseURLHosts
	}
	return []string{"hooks.slack.com"}
}

// Send posts a chat message with interactive buttons.
func (c *Channel) Send(ctx context.Context, msg store.ChatMessage, account *store.ChatAccount) error {
	if account == nil {
		return fmt.Errorf("slack: missing account")
	}
	payload := map[string]any{
		"channel": account.ExternalID,
		"text":    msg.Body,
	}
	if len(msg.Actions) > 0 {
		var elements []map[string]any
		for _, a := range msg.Actions {
			elements = append(elements, map[string]any{
				"type":      "button",
				"text":      map[string]string{"type": "plain_text", "text": a.Label},
				"action_id": a.Label,
				"value":     a.ID, // signed token
			})
		}
		payload["blocks"] = []any{
			map[string]any{
				"type": "section",
				"text": map[string]string{"type": "mrkdwn", "text": msg.Body},
			},
			map[string]any{
				"type":     "actions",
				"elements": elements,
			},
		}
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.api(ctx, "chat.postMessage", c.BotToken, payload, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack: chat.postMessage: %s", out.Error)
	}
	return nil
}

// Run opens a Socket Mode connection and processes events.
func (c *Channel) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.socketOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}

func (c *Channel) socketOnce(ctx context.Context) error {
	var open struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := c.api(ctx, "apps.connections.open", c.AppToken, map[string]any{}, &open); err != nil {
		return err
	}
	if !open.OK || open.URL == "" {
		return fmt.Errorf("slack: apps.connections.open: %s", open.Error)
	}
	dial := c.WSDial
	if dial == nil {
		dial = func(ctx context.Context, u string) (*websocket.Conn, error) {
			conn, resp, err := websocket.Dial(ctx, u, nil)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			return conn, err
		}
	}
	conn, err := dial(ctx, open.URL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var envelope struct {
			EnvelopeID string          `json:"envelope_id"`
			Type       string          `json:"type"`
			Payload    json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			continue
		}
		if envelope.EnvelopeID != "" {
			ack, _ := json.Marshal(map[string]string{"envelope_id": envelope.EnvelopeID})
			_ = conn.Write(ctx, websocket.MessageText, ack)
		}
		switch envelope.Type {
		case "interactive":
			c.handleInteractive(ctx, envelope.Payload)
		case "events_api":
			c.handleEvents(ctx, envelope.Payload)
		}
	}
}

func (c *Channel) handleInteractive(ctx context.Context, payload json.RawMessage) {
	if c.Handler == nil {
		return
	}
	var p struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Actions []struct {
			Value string `json:"value"`
		} `json:"actions"`
		Channel struct {
			ID string `json:"channel"`
		} `json:"channel"`
		ResponseURL string `json:"response_url"`
	}
	// Slack wraps payload differently; also try nested.
	if err := json.Unmarshal(payload, &p); err != nil || p.User.ID == "" {
		var wrap struct {
			Payload struct {
				User struct {
					ID string `json:"id"`
				} `json:"user"`
				Actions []struct {
					Value string `json:"value"`
				} `json:"actions"`
				Channel struct {
					ID string `json:"channel"`
				} `json:"channel"`
				ResponseURL string `json:"response_url"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(payload, &wrap); err != nil {
			return
		}
		p.User = wrap.Payload.User
		p.Actions = wrap.Payload.Actions
		p.Channel = wrap.Payload.Channel
		p.ResponseURL = wrap.Payload.ResponseURL
	}
	if len(p.Actions) == 0 {
		return
	}
	reply, _ := c.Handler.HandleAction(ctx, store.ChannelSlack, p.User.ID, p.Actions[0].Value)
	if reply != "" && p.ResponseURL != "" {
		c.postResponse(ctx, p.ResponseURL, reply)
	} else if reply != "" && p.Channel.ID != "" {
		_ = c.Send(ctx, store.ChatMessage{Body: reply}, &store.ChatAccount{ExternalID: p.Channel.ID})
	}
}

func (c *Channel) handleEvents(ctx context.Context, payload json.RawMessage) {
	if c.Handler == nil {
		return
	}
	var wrap struct {
		Event struct {
			Type    string `json:"type"`
			User    string `json:"user"`
			Text    string `json:"text"`
			Channel string `json:"channel"`
			// ChannelType is "im" for a direct message to the bot.
			ChannelType string `json:"channel_type"`
			BotID       string `json:"bot_id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &wrap); err != nil {
		return
	}
	ev := wrap.Event
	if ev.Type != "message" || ev.BotID != "" || ev.Text == "" || ev.User == "" {
		return
	}
	text := ev.Text
	if ev.ChannelType == "im" {
		text = slackLinkAsCommand(text)
	}
	reply, _ := c.Handler.HandleInboundText(ctx, store.ChannelSlack, ev.User, text)
	if reply != "" {
		_ = c.Send(ctx, store.ChatMessage{Body: reply}, &store.ChatAccount{ExternalID: ev.Channel})
	}
}

// slackLinkAsCommand turns "link CODE" (what a user can send in Slack, where
// "/link" is taken by Slack's own slash-command handling) into the "/link
// CODE" form the chat service understands. Any other text is returned as is.
func slackLinkAsCommand(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 2 && strings.EqualFold(parts[0], "link") {
		return "/link " + parts[1]
	}
	return text
}

func (c *Channel) postResponse(ctx context.Context, rawURL, text string) {
	if !allowedSlackResponseURL(rawURL, c.responseHosts()) {
		return
	}
	body, _ := json.Marshal(map[string]any{"text": text, "replace_original": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	cli := c.client()
	resp, err := cli.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func allowedSlackResponseURL(raw string, hosts []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range hosts {
		if host == strings.ToLower(h) {
			return true
		}
	}
	return false
}

func (c *Channel) api(ctx context.Context, method, token string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack: %s: HTTP %d", method, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
