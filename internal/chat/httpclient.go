package chat

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewHTTPClient returns an HTTP client that never follows redirects and has a
// hard timeout. Bot tokens often sit in URL paths; redirects must not leak them.
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ValidatePublicURL requires https, or http only for localhost / loopback.
// An empty URL is allowed (email links omitted).
func ValidatePublicURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("HAETAE_PUBLIC_URL must be an absolute URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("HAETAE_PUBLIC_URL: http is only allowed for localhost")
	default:
		return fmt.Errorf("HAETAE_PUBLIC_URL: scheme must be https (or http for localhost)")
	}
}

// RedactURLPath hides path segments that may contain bot tokens before logging.
func RedactURLPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparseable-url]"
	}
	if u.Path != "" {
		parts := strings.Split(u.Path, "/")
		for i, p := range parts {
			if strings.HasPrefix(p, "bot") && len(p) > 3 {
				parts[i] = "bot[REDACTED]"
			} else if len(p) > 20 && (strings.Contains(p, "xoxb-") || strings.Contains(p, "xapp-") || strings.Contains(p, ":")) {
				parts[i] = "[REDACTED]"
			}
		}
		u.Path = strings.Join(parts, "/")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
