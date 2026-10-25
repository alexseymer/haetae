// Package telegram bridges the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

// Handler receives inbound Telegram traffic.
type Handler interface {
	HandleInboundText(ctx context.Context, channel, externalID, text string) (reply string, err error)
	HandleAction(ctx context.Context, channel, externalID, token string) (reply string, err error)
}

// Channel talks to the Telegram Bot API over HTTPS.
type Channel struct {
	Token   string
	BaseURL string // default https://api.telegram.org; override in tests
	Client  *http.Client
	Handler Handler
	Logf    func(format string, args ...any)
}

// Name returns store.ChannelTelegram.
func (c *Channel) Name() string { return store.ChannelTelegram }

func (c *Channel) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://api.telegram.org"
}

func (c *Channel) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

// Send posts a message with an optional inline keyboard of action tokens.
func (c *Channel) Send(ctx context.Context, msg store.ChatMessage, account *store.ChatAccount) error {
	if account == nil {
		return fmt.Errorf("telegram: missing account")
	}
	chatID, err := strconv.ParseInt(account.ExternalID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad chat id: %w", err)
	}
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       msg.Body,
		"parse_mode": "HTML",
	}
	if len(msg.Actions) > 0 {
		var row []map[string]string
		for _, a := range msg.Actions {
			row = append(row, map[string]string{
				"text":          a.Label,
				"callback_data": a.ID, // already a signed token from delivery
			})
		}
		payload["reply_markup"] = map[string]any{
			"inline_keyboard": []any{row},
		}
	}
	return c.api(ctx, "sendMessage", payload, nil)
}

// Run long-polls getUpdates until ctx ends.
func (c *Channel) Run(ctx context.Context) error {
	offset := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var result struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID int64 `json:"update_id"`
				Message  *struct {
					Text string `json:"text"`
					Chat struct {
						ID int64 `json:"id"`
					} `json:"chat"`
					From struct {
						ID int64 `json:"id"`
					} `json:"from"`
				} `json:"message"`
				CallbackQuery *struct {
					ID   string `json:"id"`
					Data string `json:"data"`
					From struct {
						ID int64 `json:"id"`
					} `json:"from"`
					Message *struct {
						Chat struct {
							ID int64 `json:"id"`
						} `json:"chat"`
					} `json:"message"`
				} `json:"callback_query"`
			} `json:"result"`
		}
		err := c.api(ctx, "getUpdates", map[string]any{
			"offset":  offset,
			"timeout": 25,
		}, &result)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		for _, u := range result.Result {
			offset = int(u.UpdateID) + 1
			if c.Handler == nil {
				continue
			}
			if u.CallbackQuery != nil {
				ext := strconv.FormatInt(u.CallbackQuery.From.ID, 10)
				reply, err := c.Handler.HandleAction(ctx, store.ChannelTelegram, ext, u.CallbackQuery.Data)
				_ = c.api(ctx, "answerCallbackQuery", map[string]any{
					"callback_query_id": u.CallbackQuery.ID,
					"text":              reply,
				}, nil)
				if err == nil && reply != "" && u.CallbackQuery.Message != nil {
					_ = c.api(ctx, "sendMessage", map[string]any{
						"chat_id": u.CallbackQuery.Message.Chat.ID,
						"text":    reply,
					}, nil)
				}
				continue
			}
			if u.Message != nil && u.Message.Text != "" {
				ext := strconv.FormatInt(u.Message.From.ID, 10)
				reply, err := c.Handler.HandleInboundText(ctx, store.ChannelTelegram, ext, u.Message.Text)
				if err == nil && reply != "" {
					_ = c.api(ctx, "sendMessage", map[string]any{
						"chat_id": u.Message.Chat.ID,
						"text":    reply,
					}, nil)
				}
			}
		}
	}
}

func (c *Channel) api(ctx context.Context, method string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/%s", c.base(), c.Token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return fmt.Errorf("telegram: %s: HTTP %d: %s", method, resp.StatusCode, truncate(string(data), 200))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("telegram: decode %s: %w", method, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
