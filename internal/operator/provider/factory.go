package provider

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config selects and configures a backend.
type Config struct {
	Kind       string // "anthropic", "openai", "local"; "" or "off" disables
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// New builds the configured provider. It returns (nil, nil) when disabled.
func New(cfg Config) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Kind)) {
	case "", "off":
		return nil, nil
	case "anthropic":
		cfg.Kind = "anthropic"
		return NewAnthropic(cfg)
	case "openai":
		cfg.Kind = "openai"
		return NewOpenAICompat(cfg)
	case "local":
		cfg.Kind = "local"
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return nil, errors.New("provider: base URL is required for kind local")
		}
		return NewOpenAICompat(cfg)
	default:
		return nil, fmt.Errorf("provider: unknown kind %q", cfg.Kind)
	}
}

// resolveBase validates the base URL (applying def when empty) and reports
// whether its host is operated by the user. Plain http is only accepted for
// local hosts.
func resolveBase(cfg Config, def string) (base string, local bool, err error) {
	if cfg.Model == "" {
		return "", false, errors.New("provider: model is required")
	}
	raw := strings.TrimSpace(cfg.BaseURL)
	if raw == "" {
		raw = def
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, errors.New("provider: invalid base URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false, errors.New("provider: base URL must be http(s) with a host")
	}
	local = isLocalHost(u.Hostname())
	if u.Scheme == "http" && !(cfg.Kind == "local" && local) {
		return "", false, errors.New("provider: base URL must use https unless kind is local and the host is local")
	}
	return strings.TrimRight(u.String(), "/"), local, nil
}

// isLocalHost is true for localhost and literal loopback, private or
// link-local IPs. Names are never resolved.
func isLocalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // IPv6 zone
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
