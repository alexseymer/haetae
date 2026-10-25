// Package signal talks to a signal-cli JSON-RPC daemon when configured.
package signal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/store"
)

// Handler receives inbound Signal traffic.
type Handler interface {
	HandleInboundText(ctx context.Context, channel, externalID, text string) (reply string, err error)
	HandleSignalKeyword(ctx context.Context, channel, externalID, keyword, messageRef string) (reply string, err error)
}

// Channel sends and receives via signal-cli JSON-RPC.
type Channel struct {
	URL     string
	Client  *http.Client
	Handler Handler
	Account string // local signal account (optional)
}

// Name returns store.ChannelSignal.
func (c *Channel) Name() string { return store.ChannelSignal }

func (c *Channel) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Send delivers a plain-text message with a short ref for keyword replies.
func (c *Channel) Send(ctx context.Context, msg store.ChatMessage, account *store.ChatAccount) error {
	if account == nil {
		return fmt.Errorf("signal: missing account")
	}
	body := msg.Body
	if msg.Kind == store.KindIncident {
		body = chat.FormatSignalHint(body, msg.ID)
	}
	params := map[string]any{
		"recipient": []string{account.ExternalID},
		"message":   body,
	}
	if c.Account != "" {
		params["account"] = c.Account
	}
	_, err := c.rpc(ctx, "send", params)
	return err
}

// Run polls receive for inbound messages.
func (c *Channel) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		params := map[string]any{"timeout": 5}
		if c.Account != "" {
			params["account"] = c.Account
		}
		raw, err := c.rpc(ctx, "receive", params)
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
		c.handleReceive(ctx, raw)
	}
}

func (c *Channel) handleReceive(ctx context.Context, raw json.RawMessage) {
	if c.Handler == nil || len(raw) == 0 || string(raw) == "null" {
		return
	}
	var envelopes []struct {
		Envelope struct {
			Source      string `json:"source"`
			DataMessage *struct {
				Message string `json:"message"`
			} `json:"dataMessage"`
		} `json:"envelope"`
	}
	if err := json.Unmarshal(raw, &envelopes); err != nil {
		// single object fallback
		var one struct {
			Envelope struct {
				Source      string `json:"source"`
				DataMessage *struct {
					Message string `json:"message"`
				} `json:"dataMessage"`
			} `json:"envelope"`
		}
		if err := json.Unmarshal(raw, &one); err != nil {
			return
		}
		envelopes = append(envelopes, struct {
			Envelope struct {
				Source      string `json:"source"`
				DataMessage *struct {
					Message string `json:"message"`
				} `json:"dataMessage"`
			} `json:"envelope"`
		}{Envelope: one.Envelope})
	}
	for _, e := range envelopes {
		if e.Envelope.DataMessage == nil {
			continue
		}
		text := strings.TrimSpace(e.Envelope.DataMessage.Message)
		ext := e.Envelope.Source
		if text == "" || ext == "" {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) >= 2 {
			if kw := strings.ToLower(fields[0]); kw == "later" || kw == "mute" || kw == "ack" || kw == "acknowledge" {
				reply, _ := c.Handler.HandleSignalKeyword(ctx, store.ChannelSignal, ext, fields[0], fields[1])
				if reply != "" {
					_, _ = c.rpc(ctx, "send", map[string]any{
						"recipient": []string{ext},
						"message":   reply,
					})
				}
				continue
			}
		}
		reply, _ := c.Handler.HandleInboundText(ctx, store.ChannelSignal, ext, text)
		if reply != "" {
			_, _ = c.rpc(ctx, "send", map[string]any{
				"recipient": []string{ext},
				"message":   reply,
			})
		}
	}
}

func (c *Channel) rpc(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("signal: HTTP %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("signal: %s", out.Error.Message)
	}
	return out.Result, nil
}
