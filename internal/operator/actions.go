package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

const (
	// ActionRestartContainer is the only action kind the operator has.
	ActionRestartContainer = "restart_container"

	maxRestartsPerHour = 3
	restartWindow      = time.Hour
	// approvalTTL is how long an approval button stays valid.
	approvalTTL = 24 * time.Hour
)

// Outcomes of an ActionDecision (in addition to the three in api.go).
const (
	OutcomeRefused          = "refused"
	OutcomeAwaitingApproval = "awaiting_approval"
	OutcomeSent             = "sent"
	OutcomeFailed           = "failed"
)

// levelNow reads the organisation's autonomy level at decision time. Any
// failure or unknown value yields the lowest level, report.
func (s *Service) levelNow(ctx context.Context, org uuid.UUID) Level {
	set, err := s.cfg.Store.GetOperatorSettings(ctx, org)
	if err != nil {
		s.cfg.Log.Warn("operator: read autonomy failed", "err", err)
		return LevelReport
	}
	if l := Level(set.Autonomy); l.Valid() {
		return l
	}
	return LevelReport
}

// decide is the server's verdict on one proposed action. The model's proposal
// is only a request: the level (read now from the store), the container's
// organisation, trusted evidence of an unhealthy state and the per-container
// rate limit all have to allow it. Every decision of kind restart_container is
// written to ai_actions, refusals included.
func (s *Service) decide(ctx context.Context, c *conv, p ProposedAction) ActionDecision {
	d := ActionDecision{Action: p}
	refuse := func(a *store.AIAction, detail string) ActionDecision {
		d.Outcome, d.Detail = OutcomeRefused, detail
		if a != nil {
			a.Outcome, a.Detail = store.ActionRefused, detail
			s.audit(ctx, *a)
		}
		return d
	}

	if p.Kind != ActionRestartContainer {
		// ai_actions only records the one known kind; log the rest.
		s.cfg.Log.Warn("operator: refused unknown action kind", "kind", truncateBytes(p.Kind, 40))
		return refuse(nil, "unsupported action kind")
	}

	level := s.levelNow(ctx, c.org)
	origin := store.ActionOriginProposed
	if level == LevelAct {
		origin = store.ActionOriginAuto
	}
	row := &store.AIAction{
		OrgID: c.org, ContainerID: auditContainerID(p.ContainerID), Kind: ActionRestartContainer,
		Level: string(level), Origin: origin, Reason: p.Reason,
	}
	for _, id := range p.Evidence {
		if e := c.find(id); e != nil {
			row.EvidenceIDs = append(row.EvidenceIDs, e.Row)
		}
	}

	if level.Rank() < LevelSuggest.Rank() {
		return refuse(row, "autonomy level report allows no actions")
	}
	if err := checkContainerID(p.ContainerID); err != nil {
		return refuse(row, "invalid container id")
	}
	ct, err := s.cfg.Store.ContainerByID(ctx, c.org, p.ContainerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return refuse(row, "container not found in this organisation")
		}
		s.cfg.Log.Warn("operator: container lookup failed", "err", err)
		return refuse(row, "container lookup failed")
	}
	hostID := ct.HostID
	row.HostID = &hostID

	justified := false
	for _, id := range p.Evidence {
		if e := c.find(id); e != nil && e.Trusted && e.Kind != EvidenceLogs && evidenceShowsUnhealthy(e.Evidence, p.ContainerID) {
			justified = true
			break
		}
	}
	if !justified {
		return refuse(row, "no trusted evidence shows this container exited, restarting or unhealthy")
	}

	s.actMu.Lock()
	n, err := s.cfg.Store.CountRecentRestarts(ctx, c.org, p.ContainerID, s.cfg.Now().Add(-restartWindow))
	if err != nil {
		s.actMu.Unlock()
		s.cfg.Log.Warn("operator: restart count failed", "err", err)
		return refuse(row, "restart limit could not be checked")
	}
	if n >= maxRestartsPerHour {
		s.actMu.Unlock()
		return refuse(row, fmt.Sprintf("restart limit reached (%d per container per hour)", maxRestartsPerHour))
	}

	if level == LevelSuggest {
		s.actMu.Unlock()
		row.Outcome = store.ActionAwaitingApproval
		stored, err := s.cfg.Store.InsertAction(ctx, *row)
		if err != nil {
			s.cfg.Log.Warn("operator: audit insert failed", "err", err)
			d.Outcome, d.Detail = OutcomeRefused, "could not record the proposal"
			return d
		}
		d.Outcome, d.ApprovalID = OutcomeAwaitingApproval, stored.ID
		return d
	}

	// act: record before sending, so the rate limit sees this restart at once.
	row.Outcome = store.ActionSent
	stored, err := s.cfg.Store.InsertAction(ctx, *row)
	s.actMu.Unlock()
	if err != nil {
		s.cfg.Log.Warn("operator: audit insert failed", "err", err)
		d.Outcome, d.Detail = OutcomeRefused, "could not record the action"
		return d
	}
	outcome, detail := s.sendRestart(ctx, hostID, p.ContainerID, agentv1.Authorization_AUTHORIZATION_AUTO)
	s.finishAction(ctx, c.org, stored.ID, outcome, detail)
	if outcome == store.ActionFailed {
		d.Outcome, d.Detail = OutcomeFailed, detail
		return d
	}
	d.Outcome, d.Detail = OutcomeSent, detail
	return d
}

// auditContainerID keeps a model-supplied id short and printable for storage.
func auditContainerID(id string) string {
	return cleanText(truncateBytes(id, 128), 128)
}

func (s *Service) audit(ctx context.Context, a store.AIAction) {
	if _, err := s.cfg.Store.InsertAction(ctx, a); err != nil {
		s.cfg.Log.Warn("operator: audit insert failed", "err", err)
	}
}

func (s *Service) finishAction(ctx context.Context, org, id uuid.UUID, outcome, detail string) {
	// The command may have used up the caller's context; the audit row must
	// still be completed.
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.cfg.Store.UpdateActionOutcome(uctx, org, id, outcome, detail); err != nil {
		s.cfg.Log.Warn("operator: record action outcome failed", "err", err)
	}
}

// sendRestart sends RestartContainer and maps the result to an outcome.
func (s *Service) sendRestart(ctx context.Context, hostID uuid.UUID, containerID string, auth agentv1.Authorization) (outcome, detail string) {
	if s.cfg.Commander == nil {
		return store.ActionFailed, "no connection to the host"
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	res, err := s.cfg.Commander.Send(cctx, hostID, &agentv1.Command{
		Id:            uuid.NewString(),
		Authorization: auth,
		Kind:          &agentv1.Command_RestartContainer{RestartContainer: &agentv1.RestartContainer{ContainerId: containerID}},
	})
	switch {
	case err != nil:
		return store.ActionFailed, truncateBytes("send failed: "+err.Error(), 300)
	case res == nil:
		return store.ActionFailed, "no result from the host"
	case res.GetError() != "":
		return store.ActionFailed, truncateBytes("host refused or failed: "+res.GetError(), 300)
	}
	return store.ActionSucceeded, ""
}

// ExecuteApproved implements Operator. The chat layer has already verified
// the pressing account; this re-checks everything the server owns: the stored
// proposal is awaiting and unexpired, the level still allows it, the rate
// limit, and that the container is still in the organisation. A proposal is
// single use: it is claimed atomically before the command is sent.
func (s *Service) ExecuteApproved(ctx context.Context, orgID, approvalID uuid.UUID) error {
	act, err := s.cfg.Store.GetAction(ctx, orgID, approvalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("operator: no such proposal")
		}
		return err
	}
	if act.Outcome != store.ActionAwaitingApproval || act.Kind != ActionRestartContainer {
		return errors.New("operator: proposal is not awaiting approval")
	}
	refuse := func(msg string) error {
		_ = s.cfg.Store.RefuseAwaitingAction(ctx, orgID, approvalID, msg)
		return errors.New("operator: " + msg)
	}
	if s.cfg.Now().Sub(act.CreatedAt) > approvalTTL {
		return refuse("the proposal expired")
	}
	if s.levelNow(ctx, orgID).Rank() < LevelSuggest.Rank() {
		return refuse("autonomy level no longer allows actions")
	}
	ct, err := s.cfg.Store.ContainerByID(ctx, orgID, act.ContainerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return refuse("container no longer found in this organisation")
		}
		return err
	}

	s.actMu.Lock()
	n, err := s.cfg.Store.CountRecentRestarts(ctx, orgID, act.ContainerID, s.cfg.Now().Add(-restartWindow))
	if err != nil {
		s.actMu.Unlock()
		return err
	}
	if n >= maxRestartsPerHour {
		s.actMu.Unlock()
		return refuse("restart limit reached")
	}
	claimed, err := s.cfg.Store.ClaimApprovedAction(ctx, orgID, approvalID)
	s.actMu.Unlock()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("operator: proposal is not awaiting approval")
		}
		return err
	}
	outcome, detail := s.sendRestart(ctx, ct.HostID, claimed.ContainerID, agentv1.Authorization_AUTHORIZATION_APPROVED)
	s.finishAction(ctx, orgID, approvalID, outcome, detail)
	if outcome == store.ActionFailed {
		return errors.New("operator: restart failed: " + detail)
	}
	return nil
}

// Evidence data shape (stored in ai_evidence.data and returned to the model):
//
//	{"data": {...trusted...}, "untrusted": {...third-party strings...}}
//
// For kinds containers and findings, data.containers is a list of
// {container_id, host_id, state, health, restart_count, exit_code, oom_killed}
// with state and health from closed sets. For kind events, data.alerts is a
// list of {rule, state, container_id, ...} where container_id is resolved by
// the server from the alert's container name.
type evidenceData struct {
	Data struct {
		Containers []struct {
			ContainerID string `json:"container_id"`
			State       string `json:"state"`
			Health      string `json:"health"`
		} `json:"containers"`
		Alerts []struct {
			Rule        string `json:"rule"`
			State       string `json:"state"`
			ContainerID string `json:"container_id"`
		} `json:"alerts"`
	} `json:"data"`
}

// evidenceShowsUnhealthy reports whether trusted evidence of kind containers,
// findings or events shows exactly this container as exited, restarting,
// unhealthy or crash-looping. Evidence of kind logs, or evidence that is not
// trusted, never does.
func evidenceShowsUnhealthy(ev Evidence, containerID string) bool {
	if !ev.Trusted || containerID == "" {
		return false
	}
	switch ev.Kind {
	case EvidenceContainers, EvidenceFindings, EvidenceEvents:
	default:
		return false
	}
	var d evidenceData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return false
	}
	for _, c := range d.Data.Containers {
		if c.ContainerID != containerID {
			continue
		}
		if c.State == "exited" || c.State == "restarting" || c.Health == "unhealthy" {
			return true
		}
	}
	for _, a := range d.Data.Alerts {
		if a.ContainerID == containerID && a.State == "firing" && (a.Rule == "crash_loop" || a.Rule == "unhealthy") {
			return true
		}
	}
	return false
}
