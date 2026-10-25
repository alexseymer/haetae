package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func (a *API) registerChat(mux *http.ServeMux) {
	mux.Handle("GET /api/chat/channels", a.authenticate(http.HandlerFunc(a.handleChatChannels)))
	mux.Handle("GET /api/chat/messages", a.authenticate(http.HandlerFunc(a.handleListChatMessages)))
	mux.Handle("POST /api/chat/messages", a.authenticate(http.HandlerFunc(a.handlePostChatMessage)))
	mux.Handle("POST /api/chat/actions", a.authenticate(a.requireAdmin(http.HandlerFunc(a.handleChatAction))))
	mux.Handle("GET /api/chat/accounts", a.authenticate(http.HandlerFunc(a.handleListChatAccounts)))
	mux.Handle("DELETE /api/chat/accounts/{id}", a.authenticate(http.HandlerFunc(a.handleDeleteChatAccount)))
	mux.Handle("POST /api/chat/link", a.authenticate(http.HandlerFunc(a.handleChatLink)))
}

func (a *API) handleChatChannels(w http.ResponseWriter, r *http.Request) {
	names := []string{
		store.ChannelWeb, store.ChannelTelegram, store.ChannelSlack,
		store.ChannelEmail, store.ChannelSignal,
	}
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"name": name, "enabled": a.channelEnabled(name)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

func (a *API) handleListChatMessages(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	limit := parseLimitQuery(r, 50)
	afterStr := r.URL.Query().Get("after")
	var msgs []store.ChatMessage
	var err error
	if afterStr == "" {
		msgs, err = st.ListMessages(r.Context(), p.OrgID, nil, limit)
	} else if id, perr := uuid.Parse(afterStr); perr == nil {
		msgs, err = st.ListMessages(r.Context(), p.OrgID, &id, limit)
	} else if t, terr := time.Parse(time.RFC3339Nano, afterStr); terr == nil {
		msgs, err = a.listMessagesAfterTime(r.Context(), st, p.OrgID, t, limit)
	} else {
		writeError(w, http.StatusBadRequest, "after must be a message id or RFC3339Nano timestamp")
		return
	}
	if err != nil {
		a.log.Error("list chat messages", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, chatMessageJSON(m))
	}
	var next any
	if len(msgs) == limit && limit > 0 {
		next = msgs[len(msgs)-1].ID.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out, "next": next})
}

func (a *API) listMessagesAfterTime(ctx context.Context, st *store.Store, orgID uuid.UUID, after time.Time, limit int) ([]store.ChatMessage, error) {
	var (
		cursor *uuid.UUID
		out    []store.ChatMessage
	)
	after = after.UTC()
	for {
		batch, err := st.ListMessages(ctx, orgID, cursor, 200)
		if err != nil {
			return nil, err
		}
		for _, m := range batch {
			if !m.CreatedAt.After(after) {
				continue
			}
			out = append(out, m)
			if len(out) >= limit {
				return out, nil
			}
		}
		if len(batch) < 200 {
			return out, nil
		}
		id := batch[len(batch)-1].ID
		cursor = &id
	}
}

func (a *API) handlePostChatMessage(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat not configured")
		return
	}
	_, p := a.scoped(r)
	var in struct {
		Body string `json:"body"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		writeError(w, http.StatusBadRequest, "body is required")
		return
	}
	if utf8.RuneCountInString(in.Body) > 4000 {
		writeError(w, http.StatusBadRequest, "body must be at most 4000 characters")
		return
	}
	if err := a.ensureWebAccount(r.Context(), p.OrgID, p.UserID); err != nil {
		a.log.Error("ensure web chat account", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	reply, err := a.cfg.Chat.HandleInboundText(r.Context(), store.ChannelWeb, p.UserID.String(), in.Body)
	if err != nil {
		a.log.Error("chat inbound", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

func (a *API) handleChatAction(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Chat == nil {
		writeError(w, http.StatusServiceUnavailable, "chat not configured")
		return
	}
	_, p := a.scoped(r)
	var in struct {
		MessageID string `json:"messageId"`
		Action    string `json:"action"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	msgID, err := uuid.Parse(in.MessageID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid messageId")
		return
	}
	switch in.Action {
	case "later", "mute30d", "ack":
	default:
		writeError(w, http.StatusBadRequest, `action must be "later", "mute30d" or "ack"`)
		return
	}
	// Confirm the message belongs to this org before applying (cross-org → 404).
	msg, err := a.st.GetChatMessage(r.Context(), msgID)
	if err != nil || msg.OrgID != p.OrgID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.ensureWebAccount(r.Context(), p.OrgID, p.UserID); err != nil {
		a.log.Error("ensure web chat account", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	token := store.SignActionToken(a.cfg.Chat.Secret(), msgID, in.Action, a.now().Add(store.ActionTokenTTL))
	reply, err := a.cfg.Chat.HandleAction(r.Context(), store.ChannelWeb, p.UserID.String(), token)
	if err != nil {
		a.log.Error("chat action", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

func (a *API) handleListChatAccounts(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	list, err := st.ListChatAccounts(r.Context(), p.OrgID)
	if err != nil {
		a.log.Error("list chat accounts", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	out := make([]map[string]any, 0)
	for _, acc := range list {
		if acc.UserID != p.UserID {
			continue
		}
		out = append(out, map[string]any{
			"id":         acc.ID.String(),
			"channel":    acc.Channel,
			"externalId": maskExternalID(acc.ExternalID),
			"createdAt":  rfc3339(acc.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (a *API) handleDeleteChatAccount(w http.ResponseWriter, r *http.Request) {
	st, p := a.scoped(r)
	id, ok := parseUUIDParam(w, r.PathValue("id"))
	if !ok {
		return
	}
	acc, err := st.GetChatAccount(r.Context(), p.OrgID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("get chat account", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	if acc.UserID != p.UserID && p.Role != "admin" {
		writeError(w, http.StatusForbidden, "administrator only")
		return
	}
	// Request role has no DELETE on chat_accounts; use the server role.
	if err := a.st.DeleteChatAccount(r.Context(), p.OrgID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.log.Error("delete chat account", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleChatLink(w http.ResponseWriter, r *http.Request) {
	_, p := a.scoped(r)
	var in struct {
		Channel string `json:"channel"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	switch in.Channel {
	case store.ChannelTelegram, store.ChannelSlack, store.ChannelEmail, store.ChannelSignal:
	default:
		writeError(w, http.StatusBadRequest, "channel must be telegram, slack, email or signal")
		return
	}
	if !a.channelEnabled(in.Channel) {
		writeError(w, http.StatusConflict, "channel is not configured")
		return
	}
	// Link codes are created by the server role only (hash never readable by app role).
	code, err := a.st.CreateLinkCode(r.Context(), p.OrgID, p.UserID, in.Channel)
	if errors.Is(err, store.ErrLinkCodeRateLimit) {
		writeError(w, http.StatusTooManyRequests, "too many link codes; try again later")
		return
	}
	if err != nil {
		a.log.Error("create link code", "err", err)
		writeError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	expires := a.now().UTC().Add(store.LinkCodeTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"code":         code,
		"expiresAt":    rfc3339(expires),
		"instructions": linkInstructions(in.Channel, code),
	})
}

func (a *API) channelEnabled(name string) bool {
	if name == store.ChannelWeb {
		return true
	}
	if a.cfg.Chat == nil {
		return false
	}
	for _, n := range a.cfg.Chat.ChannelNames() {
		if n == name {
			return true
		}
	}
	return false
}

func (a *API) ensureWebAccount(ctx context.Context, orgID, userID uuid.UUID) error {
	ext := userID.String()
	if _, err := a.st.GetChatAccountByExternal(ctx, store.ChannelWeb, ext); err == nil {
		return nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_, err := a.st.CreateChatAccount(ctx, orgID, userID, store.ChannelWeb, ext)
	if errors.Is(err, store.ErrAccountTaken) {
		return nil
	}
	return err
}

func maskExternalID(s string) string {
	r := []rune(s)
	if len(r) <= 3 {
		return s
	}
	return strings.Repeat("*", len(r)-3) + string(r[len(r)-3:])
}

func linkInstructions(channel, code string) string {
	switch channel {
	case store.ChannelTelegram:
		return "Open Telegram and send /link " + code + " to the Haetae bot."
	case store.ChannelSlack:
		return "In Slack, send /link " + code + " to the Haetae app."
	case store.ChannelEmail:
		return "Reply to a Haetae email with /link " + code + " (or use the link token from the web UI)."
	case store.ChannelSignal:
		return "In Signal, send /link " + code + " to the Haetae contact."
	default:
		return "Send /link " + code + " on the channel."
	}
}

func chatMessageJSON(m store.ChatMessage) map[string]any {
	actions := m.Actions
	if actions == nil {
		actions = []store.ChatAction{}
	}
	evidence := m.Evidence
	if evidence == nil {
		evidence = []store.ChatEvidence{}
	}
	out := map[string]any{
		"id": m.ID.String(), "orgId": m.OrgID.String(), "channel": m.Channel,
		"direction": m.Direction, "kind": m.Kind, "body": m.Body,
		"actions": actions, "evidence": evidence,
		"createdAt": rfc3339(m.CreatedAt),
	}
	if m.AccountID != nil {
		out["accountId"] = m.AccountID.String()
	}
	if m.IncidentID != nil {
		out["incidentId"] = m.IncidentID.String()
	}
	return out
}
