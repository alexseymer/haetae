package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

// Fixed reply until the AI operator (M8) is enabled.
const OperatorDisabledReply = "The operator is not enabled yet; rule alerts are active."

// Notifier is the alerts.Notifier shape, defined locally so chat does not
// import internal/alerts.
type Notifier interface {
	IncidentChanged(ctx context.Context, orgID uuid.UUID, inc store.Incident, alerts []store.Alert) error
}

// Enqueuer is the briefing.Enqueuer shape.
type Enqueuer interface {
	EnqueueMessage(ctx context.Context, orgID uuid.UUID, m store.ChatMessage) error
}

// Config holds chat service configuration.
type Config struct {
	Store      *store.Store
	Channels   []Channel
	Secret     []byte // HMAC key for action tokens
	PublicURL  string // web UI base URL for email links
	Log        *slog.Logger
	PollEvery  time.Duration // delivery poll fallback; default 1s
	ClaimLimit int
	Now        func() time.Time
	Operator   OperatorClient // optional AI operator (see SetOperator)
	// ReportTimeout bounds the AI incident explanation; default 90s. Posting
	// the result has its own fresh budget.
	ReportTimeout time.Duration
}

// Service delivers chat messages and handles inbound actions.
// Delivery is at-least-once: after a successful Send, MarkDelivered is retried
// in-process and claimed ids are remembered so a reclaim does not resend.
// sent_at in the DB covers process restart; the in-memory map is TTL- and size-bounded.
type Service struct {
	st         *store.Store
	channels   map[string]Channel
	secret     []byte
	publicURL  string
	log        *slog.Logger
	pollEvery  time.Duration
	claimLimit int
	now        func() time.Time

	wake chan struct{}

	// delivered remembers message ids whose Send succeeded but MarkDelivered
	// may still be pending (AC#6: no duplicate after successful send).
	deliveredMu sync.Mutex
	delivered   map[uuid.UUID]deliveredEntry

	unlinkedReplies *tokenBucketMap
	ops             *operatorState
}

// New builds a Service. Channels without configuration should be omitted by the caller.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = time.Second
	}
	if cfg.ClaimLimit <= 0 {
		cfg.ClaimLimit = 32
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	m := make(map[string]Channel, len(cfg.Channels))
	for _, c := range cfg.Channels {
		if c != nil {
			m[c.Name()] = c
		}
	}
	ops := newOperatorState()
	if cfg.ReportTimeout > 0 {
		ops.reportTimeout = cfg.ReportTimeout
	}
	ops.op = cfg.Operator
	return &Service{
		ops:             ops,
		st:              cfg.Store,
		channels:        m,
		secret:          append([]byte(nil), cfg.Secret...),
		publicURL:       strings.TrimRight(cfg.PublicURL, "/"),
		log:             cfg.Log,
		pollEvery:       cfg.PollEvery,
		claimLimit:      cfg.ClaimLimit,
		now:             cfg.Now,
		wake:            make(chan struct{}, 1),
		delivered:       make(map[uuid.UUID]deliveredEntry),
		unlinkedReplies: newTokenBucketMap(5, time.Minute),
	}
}

// Secret returns the HMAC key (for tests and channel wiring).
func (s *Service) Secret() []byte { return s.secret }

// PublicURL returns the configured web UI base URL.
func (s *Service) PublicURL() string { return s.publicURL }

// ErrNoLinkCodeSender means the channel cannot deliver link codes.
var ErrNoLinkCodeSender = errors.New("chat: channel cannot send link codes")

// SendLinkCode delivers a link code to to (an address the caller took from the
// signed-in user's own account) through the named channel.
func (s *Service) SendLinkCode(ctx context.Context, channel, to, code string) error {
	ch, ok := s.channels[channel].(LinkCodeSender)
	if !ok {
		return ErrNoLinkCodeSender
	}
	return ch.SendLinkCode(ctx, to, code)
}

// ChannelNames returns configured channel names (tests).
func (s *Service) ChannelNames() []string {
	out := make([]string, 0, len(s.channels))
	for n := range s.channels {
		out = append(out, n)
	}
	return out
}

// IncidentChanged implements alerts.Notifier. Only high and critical incidents
// produce an immediate chat message.
func (s *Service) IncidentChanged(ctx context.Context, orgID uuid.UUID, inc store.Incident, alerts []store.Alert) error {
	if store.SeverityRank(inc.Severity) < store.SeverityRank(store.SeverityHigh) {
		return nil
	}
	actions := []store.ChatAction{
		{ID: "later", Label: "Later"},
		{ID: "mute30d", Label: "Mute 30 days"},
		{ID: "ack", Label: "Acknowledge"},
	}
	evidence := []store.ChatEvidence{
		{Label: "Host", Path: "/hosts/" + inc.HostID.String()},
		{Label: "Incident", Path: "/incidents/" + inc.ID.String()},
	}
	incID := inc.ID

	web := store.ChatMessage{
		OrgID:      orgID,
		Channel:    store.ChannelWeb,
		Direction:  "out",
		Kind:       store.KindIncident,
		Body:       renderIncidentBody(store.ChannelWeb, inc, alerts),
		Actions:    actions,
		Evidence:   evidence,
		IncidentID: &incID,
	}
	if _, err := s.st.EnqueueChatMessage(ctx, web); err != nil {
		return err
	}

	for name := range s.channels {
		if name == store.ChannelWeb {
			continue
		}
		accounts, err := s.st.ListVerifiedAccounts(ctx, orgID, name)
		if err != nil {
			return err
		}
		for _, acc := range accounts {
			msg := store.ChatMessage{
				OrgID:      orgID,
				Channel:    name,
				Direction:  "out",
				Kind:       store.KindIncident,
				Body:       renderIncidentBody(name, inc, alerts),
				Actions:    actions,
				Evidence:   evidence,
				IncidentID: &incID,
			}
			id := acc.ID
			msg.AccountID = &id
			if _, err := s.st.EnqueueChatMessage(ctx, msg); err != nil {
				return err
			}
		}
	}
	s.nudge()
	// The rule notification is out; the AI report is separate and never blocks.
	s.reportIncident(orgID, inc)
	return nil
}

func renderIncidentBody(channel string, inc store.Incident, alerts []store.Alert) string {
	title := LimitRunes(stripControls(inc.Title), maxTitleRunes)
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n", strings.ToUpper(inc.Severity), escapeFor(channel, title))
	if inc.State == "resolved" {
		b.WriteString("Resolved.\n")
	}
	n := 0
	for _, a := range alerts {
		if a.State != "firing" && inc.State != "resolved" {
			continue
		}
		summary := LimitRunes(stripControls(a.Summary), maxSummaryRunes)
		line := escapeFor(channel, summary)
		if a.Container != "" {
			line = fmt.Sprintf("%s (%s)", line, escapeFor(channel, a.Container))
		}
		b.WriteString("• ")
		b.WriteString(line)
		b.WriteByte('\n')
		n++
		if n >= 5 {
			break
		}
	}
	fmt.Fprintf(&b, "Host: /hosts/%s\nIncident: /incidents/%s", inc.HostID, inc.ID)
	return EscapeBody(channel, b.String())
}

func escapeFor(channel, s string) string {
	switch channel {
	case store.ChannelTelegram:
		return EscapeTelegram(s)
	case store.ChannelSlack:
		return EscapeSlack(s)
	default:
		return EscapePlain(s)
	}
}

// EnqueueMessage writes outbound message(s). An empty or web channel fans out
// to web plus every verified account on configured channels. When
// IdempotencyKey is set on m, each fan-out row gets a deterministic key
// (base:channel:account) so a second enqueue cannot create a duplicate.
func (s *Service) EnqueueMessage(ctx context.Context, orgID uuid.UUID, m store.ChatMessage) error {
	m.OrgID = orgID
	if m.Direction == "" {
		m.Direction = "out"
	}
	if m.Channel == "" {
		m.Channel = store.ChannelWeb
	}
	baseKey := m.IdempotencyKey
	if m.Channel == store.ChannelWeb {
		m.AccountID = nil
		m.Body = EscapeBody(store.ChannelWeb, stripControls(m.Body))
		m.IdempotencyKey = fanoutIdempotencyKey(baseKey, store.ChannelWeb, nil)
		if _, err := s.st.EnqueueChatMessage(ctx, m); err != nil {
			return err
		}
		for name := range s.channels {
			if name == store.ChannelWeb {
				continue
			}
			accounts, err := s.st.ListVerifiedAccounts(ctx, orgID, name)
			if err != nil {
				return err
			}
			for _, acc := range accounts {
				cp := m
				cp.ID = uuid.Nil
				cp.Channel = name
				id := acc.ID
				cp.AccountID = &id
				cp.Body = EscapeBody(name, stripControls(m.Body))
				cp.IdempotencyKey = fanoutIdempotencyKey(baseKey, name, &id)
				if _, err := s.st.EnqueueChatMessage(ctx, cp); err != nil {
					return err
				}
			}
		}
		s.nudge()
		return nil
	}
	m.Body = EscapeBody(m.Channel, stripControls(m.Body))
	m.IdempotencyKey = fanoutIdempotencyKey(baseKey, m.Channel, m.AccountID)
	if _, err := s.st.EnqueueChatMessage(ctx, m); err != nil {
		return err
	}
	s.nudge()
	return nil
}

func fanoutIdempotencyKey(base, channel string, accountID *uuid.UUID) string {
	if base == "" {
		return ""
	}
	acc := "-"
	if accountID != nil {
		acc = accountID.String()
	}
	return base + ":" + channel + ":" + acc
}

func (s *Service) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Ensure Service implements the local Notifier and Enqueuer shapes.
var (
	_ Notifier = (*Service)(nil)
	_ Enqueuer = (*Service)(nil)
)
