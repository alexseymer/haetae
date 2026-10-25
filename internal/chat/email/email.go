// Package email sends outbound alert mail over SMTP (STARTTLS).
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
	"unicode"

	"github.com/alexseymer/haetae/internal/store"
)

// Config for the SMTP channel.
type Config struct {
	Addr      string // host:port
	User      string
	Pass      string
	From      string
	PublicURL string
	Insecure  bool // HAETAE_SMTP_INSECURE=1: skip STARTTLS (tests only)
	// Dial optionally overrides net.DialTimeout (tests).
	Dial func(network, addr string) (net.Conn, error)
}

// Channel sends outbound email; inbound is out of scope.
type Channel struct {
	cfg Config
}

// New returns an email channel.
func New(cfg Config) *Channel {
	return &Channel{cfg: cfg}
}

// Name returns store.ChannelEmail.
func (c *Channel) Name() string { return store.ChannelEmail }

// Run is a no-op (outbound only).
func (c *Channel) Run(context.Context) error { return nil }

// Send delivers one message to the account's external_id (email address).
func (c *Channel) Send(ctx context.Context, msg store.ChatMessage, account *store.ChatAccount) error {
	if account == nil {
		return fmt.Errorf("email: missing account")
	}
	to := account.ExternalID
	subject := "Haetae alert"
	if msg.Kind == store.KindBriefing {
		subject = "Haetae briefing"
	}
	body := msg.Body
	if c.cfg.PublicURL != "" {
		body += "\n\nOpen: " + strings.TrimRight(c.cfg.PublicURL, "/") + "/"
		if msg.IncidentID != nil {
			body += "incidents/" + msg.IncidentID.String()
		}
	}
	return c.sendMail(ctx, to, subject, body)
}

func (c *Channel) sendMail(ctx context.Context, to, subject, body string) error {
	fromHeader, err := sanitizeAddress(c.cfg.From)
	if err != nil {
		return fmt.Errorf("email: from: %w", err)
	}
	toHeader, err := sanitizeAddress(to)
	if err != nil {
		return fmt.Errorf("email: to: %w", err)
	}
	subj, err := sanitizeHeaderValue(subject)
	if err != nil {
		return fmt.Errorf("email: subject: %w", err)
	}

	addr := c.cfg.Addr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		addr = addr + ":587"
	}
	dial := c.cfg.Dial
	if dial == nil {
		dial = func(network, a string) (net.Conn, error) {
			var d net.Dialer
			d.Timeout = 15 * time.Second
			return d.DialContext(ctx, network, a)
		}
	}
	conn, err := dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("email: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	defer func() { _ = client.Close() }()

	if !c.cfg.Insecure {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("email: server does not offer STARTTLS")
		}
		tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12} //nolint:gosec // G402: ServerName set
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
		}
	}
	if c.cfg.User != "" {
		auth := smtp.PlainAuth("", c.cfg.User, c.cfg.Pass, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}
	if err := client.Mail(fromHeader); err != nil {
		return err
	}
	if err := client.Rcpt(toHeader); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		fromHeader, toHeader, subj, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func sanitizeAddress(raw string) (string, error) {
	if err := rejectHeaderControls(raw); err != nil {
		return "", err
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", err
	}
	if err := rejectHeaderControls(addr.Address); err != nil {
		return "", err
	}
	return addr.Address, nil
}

func sanitizeHeaderValue(v string) (string, error) {
	if err := rejectHeaderControls(v); err != nil {
		return "", err
	}
	return v, nil
}

func rejectHeaderControls(s string) error {
	for _, r := range s {
		if r == '\r' || r == '\n' || (unicode.IsControl(r) && r != '\t') {
			return fmt.Errorf("header contains control characters")
		}
	}
	return nil
}
