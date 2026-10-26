package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 60 * time.Second
	maxBodyBytes   = 1 << 20 // 1 MiB
	userAgent      = "haetae-operator"
)

// newClient returns a client that never follows redirects and has a hard
// timeout. A caller-supplied client is copied, not modified.
func newClient(cfg Config) *http.Client {
	var c http.Client
	if cfg.HTTPClient != nil {
		c = *cfg.HTTPClient
	}
	if cfg.Timeout > 0 {
		c.Timeout = cfg.Timeout
	} else if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// scrub removes the secret from s and clips it to 200 characters.
func scrub(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "..."
	}
	return s
}

// doJSON POSTs in as JSON and decodes a 2xx answer into out. Every failure is
// one of the typed errors; messages never contain secret or the full body.
func doJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, secret string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%w: encode request: %s", ErrBadResponse, scrub(err.Error(), secret))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: build request: %s", ErrUnavailable, scrub(err.Error(), secret))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL
		}
		return fmt.Errorf("%w: request failed: %s", ErrUnavailable, scrub(err.Error(), secret))
	}
	defer func() { _ = resp.Body.Close() }()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	tooBig := len(body) > maxBodyBytes

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("%w: status %d", ErrAuth, code)
	case code == http.StatusTooManyRequests:
		return fmt.Errorf("%w: status %d", ErrRateLimited, code)
	case code >= 500:
		return fmt.Errorf("%w: status %d", ErrUnavailable, code)
	case code >= 300 && code < 400:
		return fmt.Errorf("%w: unexpected redirect (status %d)", ErrBadResponse, code)
	default:
		return fmt.Errorf("%w: status %d: %s", ErrBadResponse, code, scrub(string(body), secret))
	}
	if rerr != nil {
		return fmt.Errorf("%w: read response: %s", ErrUnavailable, scrub(rerr.Error(), secret))
	}
	if tooBig {
		return fmt.Errorf("%w: response larger than %d bytes", ErrBadResponse, maxBodyBytes)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: invalid JSON: %s", ErrBadResponse, scrub(err.Error(), secret))
	}
	return nil
}

func rawOrEmptyObject(r json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(r)) == 0 {
		return json.RawMessage(`{}`)
	}
	return r
}

func schemaOrObject(r json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(r)) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return r
}

func clampMaxTokens(n int) int {
	if n <= 0 {
		return 1024
	}
	if n > 4096 {
		return 4096
	}
	return n
}
