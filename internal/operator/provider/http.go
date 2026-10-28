package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
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

const maxDetailRunes = 160

// cleanDetail removes the secret from s, strips control characters, collapses
// whitespace and clips the result to 160 characters.
func cleanDetail(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxDetailRunes {
		s = string(r[:maxDetailRunes-3]) + "..."
	}
	return s
}

// errorMessage returns the message of an API error body such as
// {"error":{"message":"..."}}, {"error":"..."} or {"message":"..."}, or "".
func errorMessage(body []byte) string {
	var w struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &w) != nil {
		return ""
	}
	if len(w.Error) > 0 {
		var o struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(w.Error, &o) == nil && o.Message != "" {
			return o.Message
		}
		var s string
		if json.Unmarshal(w.Error, &s) == nil && s != "" {
			return s
		}
	}
	return w.Message
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// doJSON POSTs in as JSON and decodes a 2xx answer into out. Every failure is
// a *Error; messages never contain secret or the full body.
func doJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, secret string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return &Error{Kind: ErrBadResponse, Detail: "encode request: " + cleanDetail(err.Error(), secret)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL
		}
		return &Error{Kind: ErrUnavailable, Detail: "build request: " + cleanDetail(err.Error(), secret)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		timeout := isTimeout(err)
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL
		}
		d := cleanDetail(err.Error(), secret)
		if timeout {
			d = "timeout awaiting response"
		}
		return &Error{Kind: ErrUnavailable, Detail: d, Timeout: timeout}
	}
	defer func() { _ = resp.Body.Close() }()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	tooBig := len(body) > maxBodyBytes

	code := resp.StatusCode
	if code < 200 || code >= 300 {
		e := &Error{Status: code}
		if !tooBig {
			e.Detail = cleanDetail(errorMessage(body), secret)
		}
		switch {
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			e.Kind = ErrAuth
		case code == http.StatusTooManyRequests:
			e.Kind = ErrRateLimited
		case code >= 500:
			e.Kind = ErrUnavailable
		case code >= 300 && code < 400:
			e.Kind, e.Detail = ErrBadResponse, "unexpected redirect"
		default:
			e.Kind = ErrBadResponse
		}
		return e
	}
	if rerr != nil {
		return &Error{Kind: ErrUnavailable, Status: code, Detail: "read response: " + cleanDetail(rerr.Error(), secret), Timeout: isTimeout(rerr)}
	}
	if tooBig {
		return &Error{Kind: ErrBadResponse, Status: code, Detail: fmt.Sprintf("response larger than %d bytes", maxBodyBytes)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Kind: ErrBadResponse, Status: code, Detail: "invalid JSON: " + cleanDetail(err.Error(), secret)}
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
