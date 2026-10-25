package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

const (
	replyLinkOK         = "Linked. You will receive alerts here."
	replyLinkBad        = "That link code is not valid."
	replyUnlinked       = "Link your account first with /link CODE from the web UI."
	replyActionRejected = "Action not allowed."
	replyActionLater    = "Snoozed."
	replyActionMuted    = "Muted for 30 days."
	replyActionAck      = "Acknowledged."
	replyAlreadyMuted   = "Already muted."
)

// HandleLink consumes a /link CODE from an external identity.
func (s *Service) HandleLink(ctx context.Context, channel, externalID, code string) (string, error) {
	orgID, userID, wantChannel, err := s.st.ConsumeLinkCode(ctx, code)
	if err != nil {
		s.auditReject(channel, externalID, "link_invalid", err)
		return replyLinkBad, nil
	}
	if wantChannel != channel {
		s.auditReject(channel, externalID, "link_wrong_channel", nil)
		return replyLinkBad, nil
	}
	if _, err := s.st.CreateChatAccount(ctx, orgID, userID, channel, externalID); err != nil {
		if errors.Is(err, store.ErrAccountTaken) {
			s.auditReject(channel, externalID, "link_takeover_refused", err)
			return replyLinkBad, nil
		}
		return "", err
	}
	return replyLinkOK, nil
}

// HandleAction processes a signed button token from a channel identity.
func (s *Service) HandleAction(ctx context.Context, channel, externalID, token string) (string, error) {
	messageID, action, err := store.VerifyActionToken(s.secret, token, s.now())
	if err != nil {
		s.auditReject(channel, externalID, "action_token_invalid", err)
		return replyActionRejected, nil
	}
	return s.applyAction(ctx, channel, externalID, messageID, action)
}

// HandleSignalKeyword handles later/mute/ack with a short message ref.
func (s *Service) HandleSignalKeyword(ctx context.Context, channel, externalID, keyword, messageRef string) (string, error) {
	action := mapKeyword(keyword)
	if action == "" {
		return "", nil
	}
	acc, err := s.st.GetChatAccountByExternal(ctx, channel, externalID)
	if err != nil {
		s.auditReject(channel, externalID, "action_unlinked", err)
		return replyActionRejected, nil
	}
	msg, err := s.st.FindOutboundByShortRef(ctx, acc.OrgID, channel, messageRef)
	if err != nil {
		if id, perr := uuid.Parse(strings.TrimSpace(messageRef)); perr == nil {
			msg, err = s.st.GetChatMessage(ctx, id)
		}
		if err != nil {
			s.auditReject(channel, externalID, "action_ref_invalid", err)
			return replyActionRejected, nil
		}
	}
	return s.applyAction(ctx, channel, externalID, msg.ID, action)
}

func mapKeyword(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "later":
		return "later"
	case "mute":
		return "mute30d"
	case "ack", "acknowledge":
		return "ack"
	}
	return ""
}

func (s *Service) applyAction(ctx context.Context, channel, externalID string, messageID uuid.UUID, action string) (string, error) {
	acc, err := s.st.GetChatAccountByExternal(ctx, channel, externalID)
	if err != nil {
		s.auditReject(channel, externalID, "action_unlinked", err)
		return replyActionRejected, nil
	}
	msg, err := s.st.GetChatMessage(ctx, messageID)
	if err != nil {
		s.auditReject(channel, externalID, "action_unknown_message", err)
		return replyActionRejected, nil
	}
	if msg.OrgID != acc.OrgID {
		s.auditReject(channel, externalID, "action_wrong_org", nil)
		return replyActionRejected, nil
	}
	role, err := s.st.UserRole(ctx, acc.OrgID, acc.UserID)
	if err != nil || role != "admin" {
		s.auditReject(channel, externalID, "action_not_admin", err)
		return replyActionRejected, nil
	}

	switch action {
	case "later":
		if _, err := s.st.InsertInbound(ctx, store.ChatMessage{
			OrgID:      msg.OrgID,
			Channel:    channel,
			AccountID:  &acc.ID,
			Kind:       store.KindAction,
			Body:       "later",
			IncidentID: msg.IncidentID,
		}); err != nil {
			return "", err
		}
		return replyActionLater, nil
	case "mute30d":
		return s.muteIncident(ctx, acc, msg)
	case "ack":
		if _, err := s.st.InsertInbound(ctx, store.ChatMessage{
			OrgID:      msg.OrgID,
			Channel:    channel,
			AccountID:  &acc.ID,
			Kind:       store.KindAction,
			Body:       "ack",
			IncidentID: msg.IncidentID,
		}); err != nil {
			return "", err
		}
		return replyActionAck, nil
	default:
		s.auditReject(channel, externalID, "action_unknown", nil)
		return replyActionRejected, nil
	}
}

func (s *Service) muteIncident(ctx context.Context, acc store.ChatAccount, msg store.ChatMessage) (string, error) {
	if msg.IncidentID == nil {
		s.auditReject(acc.Channel, acc.ExternalID, "action_no_incident", nil)
		return replyActionRejected, nil
	}
	inc, alerts, err := s.st.GetIncident(ctx, msg.OrgID, *msg.IncidentID)
	if err != nil {
		s.auditReject(acc.Channel, acc.ExternalID, "action_incident_missing", err)
		return replyActionRejected, nil
	}
	until := s.now().Add(30 * 24 * time.Hour)
	hostID := inc.HostID
	uid := acc.UserID

	rules := uniqueRules(alerts)
	if len(rules) == 0 {
		rules = []string{""}
	}
	for _, rule := range rules {
		if _, err := s.st.CreateMute(ctx, store.Mute{
			OrgID:     msg.OrgID,
			HostID:    &hostID,
			Rule:      rule,
			Until:     until,
			CreatedBy: &uid,
		}); err != nil {
			return "", err
		}
	}
	// Idempotent marker row.
	if _, err := s.st.InsertInbound(ctx, store.ChatMessage{
		OrgID:      msg.OrgID,
		Channel:    acc.Channel,
		AccountID:  &acc.ID,
		Kind:       store.KindAction,
		Body:       "mute30d",
		IncidentID: msg.IncidentID,
	}); err != nil {
		return "", err
	}
	_ = replyAlreadyMuted // reserved for future mute-exists check
	return replyActionMuted, nil
}

func uniqueRules(alerts []store.Alert) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, a := range alerts {
		if a.Rule == "" {
			continue
		}
		if _, ok := seen[a.Rule]; ok {
			continue
		}
		seen[a.Rule] = struct{}{}
		out = append(out, a.Rule)
	}
	return out
}

// HandleInboundText stores free text from a linked account and returns the
// fixed operator-disabled reply. Unlinked senders get only a generic reply.
func (s *Service) HandleInboundText(ctx context.Context, channel, externalID, text string) (string, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(strings.ToLower(text), "/link") {
		parts := strings.Fields(text)
		if len(parts) < 2 {
			return replyLinkBad, nil
		}
		return s.HandleLink(ctx, channel, externalID, parts[1])
	}
	acc, err := s.st.GetChatAccountByExternal(ctx, channel, externalID)
	if err != nil {
		s.auditReject(channel, externalID, "inbound_unlinked", err)
		return replyUnlinked, nil
	}
	if _, err := s.st.InsertInbound(ctx, store.ChatMessage{
		OrgID:     acc.OrgID,
		Channel:   channel,
		AccountID: &acc.ID,
		Kind:      store.KindText,
		Body:      LimitRunes(stripControls(text), maxBodyRunes),
	}); err != nil {
		return "", err
	}
	return OperatorDisabledReply, nil
}

func (s *Service) auditReject(channel, externalID, reason string, err error) {
	attrs := []any{"channel", channel, "external_id", externalID, "reason", reason}
	if err != nil {
		attrs = append(attrs, "err", err.Error())
	}
	s.log.Info("chat action rejected", attrs...)
}

// ShortRef returns an 8-character reference for Signal keyword replies.
func ShortRef(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")[:8]
}

// FormatSignalHint appends a short ref line for Signal (no buttons).
func FormatSignalHint(body string, id uuid.UUID) string {
	return fmt.Sprintf("%s\nRef: %s (reply: later|mute|ack %s)", body, ShortRef(id), ShortRef(id))
}
