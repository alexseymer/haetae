package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

// OperatorClient is the part of operator.Operator the chat service uses.
type OperatorClient interface {
	Available(ctx context.Context, orgID uuid.UUID) (ok bool, reason string)
	Ask(ctx context.Context, orgID uuid.UUID, question string) (operator.Answer, error)
	ExplainIncident(ctx context.Context, orgID, incidentID uuid.UUID) (operator.Answer, error)
	ExecuteApproved(ctx context.Context, orgID, approvalID uuid.UUID) error
}

const (
	askTimeout       = 90 * time.Second
	availableTimeout = 5 * time.Second
	executeTimeout   = 30 * time.Second
	reportTimeout    = 90 * time.Second
	postTimeout      = 15 * time.Second
	reportDebounce   = 10 * time.Minute
	maxAskInFlight   = 8
	maxReportWorkers = 2
	maxReportEntries = 4096
	debugLogEvery    = time.Minute
	replyBusy        = "The operator is busy; try again shortly."
)

// operatorState holds the optional operator and the bounded background work.
type operatorState struct {
	mu sync.RWMutex
	op OperatorClient

	askSem    chan struct{}
	reportSem chan struct{}

	reportMu   sync.Mutex
	reportSeen map[uuid.UUID]time.Time

	reportTimeout time.Duration

	lastDebug atomic.Int64 // unix nanos of the last debug log
}

func newOperatorState() *operatorState {
	return &operatorState{
		askSem:     make(chan struct{}, maxAskInFlight),
		reportSem:  make(chan struct{}, maxReportWorkers),
		reportSeen: make(map[uuid.UUID]time.Time),

		reportTimeout: reportTimeout,
	}
}

// SetOperator sets (or clears, with nil) the AI operator.
func (s *Service) SetOperator(op OperatorClient) {
	s.ops.mu.Lock()
	s.ops.op = op
	s.ops.mu.Unlock()
}

func (s *Service) operator() OperatorClient {
	s.ops.mu.RLock()
	defer s.ops.mu.RUnlock()
	return s.ops.op
}

func unavailableReply(reason string) string {
	reason = LimitRunes(stripControls(reason), maxFieldRunes)
	if reason == "" {
		reason = "not available"
	}
	return fmt.Sprintf("The operator is unavailable (%s); rule alerts are active.", reason)
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, operator.ErrCapped):
		return "daily usage cap reached"
	case errors.Is(err, operator.ErrDisabled):
		return "turned off"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	return "provider unreachable"
}

// callTimeout runs fn in its own goroutine and returns when it finishes or
// ctx ends, so an operator that ignores its context cannot hold a caller.
func callTimeout[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				var zero T
				ch <- result{zero, fmt.Errorf("operator panic: %v", r)}
			}
		}()
		v, err := fn(ctx)
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

type availability struct {
	ok     bool
	reason string
}

func (s *Service) checkAvailable(ctx context.Context, op OperatorClient, orgID uuid.UUID) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, availableTimeout)
	defer cancel()
	a, err := callTimeout(cctx, func(c context.Context) (availability, error) {
		ok, reason := op.Available(c, orgID)
		return availability{ok, reason}, nil
	})
	if err != nil {
		return false, "not responding"
	}
	return a.ok, a.reason
}

// debugLimited logs at debug level at most once per debugLogEvery.
func (s *Service) debugLimited(msg string, args ...any) {
	now := s.now().UnixNano()
	last := s.ops.lastDebug.Load()
	if now-last < int64(debugLogEvery) || !s.ops.lastDebug.CompareAndSwap(last, now) {
		return
	}
	s.log.Debug(msg, args...)
}

// askOperator is called after the account is verified and the inbound stored.
// It returns an immediate reply, or "" when the answer will arrive as an
// outbound message (the operator call runs in its own goroutine so a slow
// operator never blocks the channel's receive loop).
func (s *Service) askOperator(ctx context.Context, acc store.ChatAccount, channel, question string) string {
	op := s.operator()
	if op == nil {
		return OperatorDisabledReply
	}
	if ok, reason := s.checkAvailable(ctx, op, acc.OrgID); !ok {
		return unavailableReply(reason)
	}
	select {
	case s.ops.askSem <- struct{}{}:
	default:
		return replyBusy
	}
	go func() {
		defer func() { <-s.ops.askSem }()
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("operator ask panic", "panic", r)
			}
		}()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), askTimeout)
		defer cancel()
		ans, err := callTimeout(cctx, func(c context.Context) (operator.Answer, error) {
			return op.Ask(c, acc.OrgID, question)
		})
		if err != nil {
			s.debugLimited("operator ask failed", "err", err)
			s.sendToAccount(acc, channel, store.KindAnswer, unavailableReply(reasonFor(err)), nil)
			return
		}
		body, ev := renderAnswer(channel, s.publicURL, ans)
		s.sendToAccount(acc, channel, store.KindAnswer, body, ev)
		s.postApprovals(acc.OrgID, nil, ans)
	}()
	return ""
}

// sendToAccount posts one outbound message to the asking account (web: the
// org's web chat; other channels: that account only).
func (s *Service) sendToAccount(acc store.ChatAccount, channel, kind, body string, ev []store.ChatEvidence) {
	m := store.ChatMessage{
		OrgID:    acc.OrgID,
		Channel:  channel,
		Kind:     kind,
		Body:     body,
		Evidence: ev,
	}
	if channel != store.ChannelWeb {
		id := acc.ID
		m.AccountID = &id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.enqueueOut(ctx, m); err != nil {
		s.log.Warn("enqueue operator reply", "err", err)
	}
}

// enqueueOut escapes the body for its channel and enqueues one message
// without fan-out.
func (s *Service) enqueueOut(ctx context.Context, m store.ChatMessage) (store.ChatMessage, error) {
	m.Direction = "out"
	m.Body = escapeLines(m.Channel, m.Body)
	out, err := s.st.EnqueueChatMessage(ctx, m)
	if err != nil {
		return out, err
	}
	s.nudge()
	return out, nil
}

// enqueueOutPre gives the message its id, runs pre (which may record state
// keyed by that id) and then enqueues it.
func (s *Service) enqueueOutPre(ctx context.Context, m store.ChatMessage, pre func(store.ChatMessage) error) (store.ChatMessage, error) {
	if pre != nil {
		m.ID = uuid.New()
		if err := pre(m); err != nil {
			return store.ChatMessage{}, err
		}
	}
	return s.enqueueOut(ctx, m)
}

// escapeLines escapes each line for the channel and keeps line breaks (the
// shared stripControls would flatten them).
func escapeLines(channel, s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = EscapeBody(channel, l)
	}
	return LimitRunes(strings.Join(lines, "\n"), maxBodyRunes)
}

// broadcast enqueues build(channel) to the web chat and to every verified
// account on every configured channel. It returns the enqueued messages.
func (s *Service) broadcast(ctx context.Context, orgID uuid.UUID, build func(channel string) store.ChatMessage, pre func(store.ChatMessage) error) ([]store.ChatMessage, error) {
	var out []store.ChatMessage
	web := build(store.ChannelWeb)
	web.OrgID, web.Channel, web.AccountID = orgID, store.ChannelWeb, nil
	m, err := s.enqueueOutPre(ctx, web, pre)
	if err != nil {
		return out, err
	}
	out = append(out, m)
	for name := range s.channels {
		if name == store.ChannelWeb {
			continue
		}
		accounts, err := s.st.ListVerifiedAccounts(ctx, orgID, name)
		if err != nil {
			return out, err
		}
		for _, acc := range accounts {
			cp := build(name)
			cp.OrgID, cp.Channel = orgID, name
			id := acc.ID
			cp.AccountID = &id
			m, err := s.enqueueOutPre(ctx, cp, pre)
			if err != nil {
				return out, err
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// renderAnswer renders an operator answer for a channel. Web carries evidence
// as structured links; other channels also get full links in the text.
func renderAnswer(channel, publicURL string, ans operator.Answer) (string, []store.ChatEvidence) {
	var b strings.Builder
	b.WriteString(ans.Text())
	ev := evidenceLinks(ans.Evidence)
	if channel != store.ChannelWeb && len(ev) > 0 {
		b.WriteString("\n")
		for _, e := range ev {
			fmt.Fprintf(&b, "\n%s: %s", e.Label, publicURL+e.Path)
		}
	}
	return b.String(), ev
}

func evidenceLinks(in []operator.Evidence) []store.ChatEvidence {
	var out []store.ChatEvidence
	for _, e := range in {
		if e.Link == "" || !strings.HasPrefix(e.Link, "/") || strings.HasPrefix(e.Link, "//") {
			continue
		}
		label := LimitRunes(stripControls(e.Title), maxFieldRunes)
		if label == "" {
			label = e.ID
		}
		out = append(out, store.ChatEvidence{Label: label, Path: e.Link})
	}
	return out
}

// postApprovals posts an approve/decline message for every proposal awaiting
// approval. incidentID is attached to the message when set.
func (s *Service) postApprovals(orgID uuid.UUID, incidentID *uuid.UUID, ans operator.Answer) {
	for _, d := range ans.Actions {
		if d.Outcome != "awaiting_approval" || d.ApprovalID == uuid.Nil {
			continue
		}
		s.postApproval(orgID, incidentID, d, ans.Evidence)
	}
}

func (s *Service) postApproval(orgID uuid.UUID, incidentID *uuid.UUID, d operator.ActionDecision, all []operator.Evidence) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cited := make(map[string]bool, len(d.Action.Evidence))
	for _, id := range d.Action.Evidence {
		cited[id] = true
	}
	var evs []operator.Evidence
	for _, e := range all {
		if cited[e.ID] {
			evs = append(evs, e)
		}
	}
	ev := evidenceLinks(evs)
	const askAdmin = "Ask an administrator to approve."
	otherApprovalChannel := false
	for name := range s.channels {
		if name != store.ChannelSignal {
			otherApprovalChannel = true
		}
	}
	build := func(channel string, buttons bool, note string) store.ChatMessage {
		kind := d.Action.Kind
		if kind == "restart_container" || kind == "" {
			kind = "restart"
		}
		body := fmt.Sprintf("Haetae proposes: %s %s because %s",
			LimitRunes(stripControls(kind), 40),
			LimitRunes(stripControls(d.Action.ContainerID), maxFieldRunes),
			LimitRunes(stripControls(d.Action.Reason), maxSummaryRunes))
		if len(d.Action.Evidence) > 0 {
			body += " [" + LimitRunes(stripControls(strings.Join(d.Action.Evidence, ", ")), maxFieldRunes) + "]"
		}
		if channel != store.ChannelWeb {
			for _, e := range ev {
				body += fmt.Sprintf("\n%s: %s", e.Label, s.publicURL+e.Path)
			}
		}
		m := store.ChatMessage{
			Kind:       store.KindAnswer,
			Body:       body,
			Evidence:   ev,
			IncidentID: incidentID,
		}
		if buttons {
			m.Actions = []store.ChatAction{
				{ID: store.ActionApprove, Label: "Approve"},
				{ID: store.ActionDecline, Label: "Decline"},
			}
		} else {
			m.Body += "\n" + note
		}
		return m
	}
	// Buttons go only to verified accounts of organisation admins. The web
	// chat is visible to every member, so it gets the text without buttons.
	web := build(store.ChannelWeb, false, askAdmin)
	web.OrgID, web.Channel, web.AccountID = orgID, store.ChannelWeb, nil
	if _, err := s.enqueueOut(ctx, web); err != nil {
		s.log.Warn("post approval", "err", err)
		return
	}
	for name := range s.channels {
		if name == store.ChannelWeb {
			continue
		}
		accounts, err := s.st.ListVerifiedAccounts(ctx, orgID, name)
		if err != nil {
			s.log.Warn("post approval", "err", err)
			return
		}
		admins, err := s.st.ListVerifiedAdminAccounts(ctx, orgID, name)
		if err != nil {
			s.log.Warn("post approval", "err", err)
			return
		}
		isAdmin := make(map[uuid.UUID]bool, len(admins))
		for _, a := range admins {
			isAdmin[a.ID] = true
		}
		for _, acc := range accounts {
			// Signal replies are keyword-only (later/mute/ack), so an
			// approval cannot be pressed there: point admins elsewhere.
			buttons := isAdmin[acc.ID] && name != store.ChannelSignal
			note := askAdmin
			if isAdmin[acc.ID] && name == store.ChannelSignal && otherApprovalChannel {
				note = "Approve it in the web interface or another chat channel."
			}
			cp := build(name, buttons, note)
			cp.OrgID, cp.Channel = orgID, name
			id := acc.ID
			cp.AccountID = &id
			var pre func(store.ChatMessage) error
			if buttons {
				pre = func(m store.ChatMessage) error {
					return s.st.LinkMessageApproval(ctx, orgID, m.ID, d.ApprovalID)
				}
			}
			if _, err := s.enqueueOutPre(ctx, cp, pre); err != nil {
				s.log.Warn("post approval", "err", err)
				return
			}
		}
	}
}

// reportIncident starts the background AI report for an incident. It never
// blocks the caller: the debounce check is a map lookup and the work runs in
// a goroutine bounded to maxReportWorkers.
func (s *Service) reportIncident(orgID uuid.UUID, inc store.Incident) {
	op := s.operator()
	if op == nil || inc.State == "resolved" {
		return
	}
	now := s.now()
	s.ops.reportMu.Lock()
	if last, ok := s.ops.reportSeen[inc.ID]; ok && now.Sub(last) < reportDebounce {
		s.ops.reportMu.Unlock()
		return
	}
	if len(s.ops.reportSeen) >= maxReportEntries {
		for id, t := range s.ops.reportSeen {
			if now.Sub(t) >= reportDebounce {
				delete(s.ops.reportSeen, id)
			}
		}
		if len(s.ops.reportSeen) >= maxReportEntries {
			s.ops.reportMu.Unlock()
			return
		}
	}
	s.ops.reportSeen[inc.ID] = now
	s.ops.reportMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("incident report panic", "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), s.ops.reportTimeout)
		defer cancel()
		select {
		case s.ops.reportSem <- struct{}{}:
			defer func() { <-s.ops.reportSem }()
		case <-ctx.Done():
			return
		}
		if ok, reason := s.checkAvailable(ctx, op, orgID); !ok {
			s.debugLimited("incident report skipped", "reason", reason)
			return
		}
		ans, err := callTimeout(ctx, func(c context.Context) (operator.Answer, error) {
			return op.ExplainIncident(c, orgID, inc.ID)
		})
		if err != nil {
			s.debugLimited("incident report failed", "err", err)
			return
		}
		// A slow but successful explanation must still be delivered: posting
		// gets its own budget instead of what is left of the explain deadline.
		pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), postTimeout)
		defer pcancel()
		title := LimitRunes(stripControls(inc.Title), maxTitleRunes)
		incID := inc.ID
		_, err = s.broadcast(pctx, orgID, func(channel string) store.ChatMessage {
			if ans.CauseNotFound || len(ans.Statements) == 0 {
				return store.ChatMessage{
					Kind:       store.KindAnswer,
					Body:       "Haetae: cause not found for " + title,
					IncidentID: &incID,
				}
			}
			body, ev := renderAnswer(channel, s.publicURL, ans)
			return store.ChatMessage{
				Kind:       store.KindAnswer,
				Body:       "Haetae: " + body,
				Evidence:   ev,
				IncidentID: &incID,
			}
		}, nil)
		if err != nil {
			s.debugLimited("incident report post failed", "err", err)
			return
		}
		s.postApprovals(orgID, &incID, ans)
	}()
}

// applyApproval handles a press of the approve or decline button. The caller
// already checked the linked account, org match and admin role.
func (s *Service) applyApproval(ctx context.Context, acc store.ChatAccount, msg store.ChatMessage, action string) (string, error) {
	op := s.operator()
	if action == store.ActionApprove && op == nil {
		return unavailableReply("not configured"), nil
	}
	inbound := store.ChatMessage{
		OrgID:     msg.OrgID,
		Channel:   acc.Channel,
		AccountID: &acc.ID,
		Kind:      store.KindAction,
		Body:      strings.TrimSuffix(action, "_action"),
	}
	approvalID, err := s.st.ApplyApprovalAction(ctx, msg.ID, action, inbound)
	if err != nil {
		if errors.Is(err, store.ErrActionAlreadyUsed) || errors.Is(err, store.ErrApprovalAlreadyHandled) {
			return replyAlreadyHandled, nil
		}
		s.auditReject(acc.Channel, acc.ExternalID, "action_consume", err)
		return replyActionRejected, nil
	}
	if action == store.ActionDecline {
		return "Declined.", nil
	}
	cctx, cancel := context.WithTimeout(ctx, executeTimeout)
	defer cancel()
	_, err = callTimeout(cctx, func(c context.Context) (struct{}, error) {
		return struct{}{}, op.ExecuteApproved(c, msg.OrgID, approvalID)
	})
	if errors.Is(err, operator.ErrNoLongerNeeded) {
		return "No longer needed: the container is no longer exited, restarting or unhealthy. Nothing was sent.", nil
	}
	if err != nil {
		s.log.Warn("execute approved action", "approval", approvalID, "err", err)
		return "Approved, but the action could not be run: " + LimitRunes(stripControls(err.Error()), maxFieldRunes), nil
	}
	return "Approved. The action was sent.", nil
}
