package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/store"
)

const incidentPrompt = "Explain what happened in this incident and the most likely cause, using only evidence."

// ExplainIncident implements Operator. The incident and its alerts are loaded
// through org-scoped store methods and handed to the model as a trusted
// evidence record of kind events (third-party strings under "untrusted"); the
// rest is the same tool loop, validation and action gating as Ask. A resolved
// incident is explained as a post-mortem and never leads to a proposal.
func (s *Service) ExplainIncident(ctx context.Context, orgID, incidentID uuid.UUID) (Answer, error) {
	ans, err := s.explainIncident(ctx, orgID, incidentID)
	s.logFailure("explain_incident", err)
	return ans, err
}

func (s *Service) explainIncident(ctx context.Context, orgID, incidentID uuid.UUID) (Answer, error) {
	if err := s.gate(ctx, orgID); err != nil {
		return Answer{}, err
	}
	inc, alerts, err := s.cfg.Store.GetIncident(ctx, orgID, incidentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Answer{}, fmt.Errorf("operator: incident not found: %w", err)
		}
		return Answer{}, fmt.Errorf("operator: load incident: %w", err)
	}
	if inc.OrgID != orgID {
		return Answer{}, errors.New("operator: incident not found")
	}
	out, err := s.incidentEvidence(ctx, orgID, inc, alerts)
	if err != nil {
		return Answer{}, err
	}
	c := &conv{id: uuid.New(), org: orgID}
	result, ok := s.addEvidence(ctx, c, out)
	if !ok {
		return Answer{ConversationID: c.id}, errors.New("operator: could not store incident evidence")
	}

	// A new_cve incident also gets the finding details of its host: fixed
	// versions and image-update availability, so the report says what to
	// upgrade. Trusted numbers, third-party names under "untrusted".
	var cveResults []string
	var cveCalls []provider.ToolCall
	for i, o := range s.incidentFindingEvidence(ctx, orgID, inc, alerts) {
		res, ok := s.addEvidence(ctx, c, o)
		if !ok {
			continue
		}
		cveCalls = append(cveCalls, provider.ToolCall{ID: fmt.Sprintf("seed_%d", i+1), Name: "list_findings",
			Args: json.RawMessage(fmt.Sprintf(`{"container_id":%q}`, o.subjectID))})
		cveResults = append(cveResults, res)
	}

	// The seed looks to the model like the result of a list_events call, so
	// the usual rule applies: third-party text only as "untrusted" values.
	user, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleUser, Text: incidentPrompt})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	calls := append([]provider.ToolCall{
		{ID: "seed_0", Name: "list_events", Args: json.RawMessage(fmt.Sprintf(`{"from":%q,"to":%q}`,
			inc.OpenedAt.UTC().Format(time.RFC3339), s.cfg.Now().UTC().Format(time.RFC3339)))},
	}, cveCalls...)
	call, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	res, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleTool, ToolCallID: "seed_0", Text: result})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	msgs := []provider.Message{user, call, res}
	for i, r := range cveResults {
		m, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleTool, ToolCallID: cveCalls[i].ID, Text: r})
		if err != nil {
			return Answer{ConversationID: c.id}, err
		}
		msgs = append(msgs, m)
	}
	return s.runConv(ctx, c, msgs, inc.State == "open")
}

// maxIncidentFindingRecords bounds the finding records added to a new_cve
// incident report.
const maxIncidentFindingRecords = 3

// incidentFindingEvidence returns the per-container finding records of the
// incident's host when one of its alerts is a new_cve alert. Failures are not
// fatal: the report then simply lacks the details.
func (s *Service) incidentFindingEvidence(ctx context.Context, org uuid.UUID, inc store.Incident, alerts []store.Alert) []*toolOut {
	isCVE := false
	for _, a := range alerts {
		isCVE = isCVE || a.Rule == store.RuleNewCVE
	}
	if !isCVE {
		return nil
	}
	in, err := s.securityDigestInput(ctx, org, SecurityScope{HostID: inc.HostID})
	if err != nil {
		s.cfg.Log.Warn("operator: incident finding details failed", "err", err)
		return nil
	}
	var out []*toolOut
	for _, o := range in.outs {
		if o.subjectType == "container" && o.kind == EvidenceFindings && len(out) < maxIncidentFindingRecords {
			out = append(out, o)
		}
	}
	return out
}

// incidentEvidence builds the events evidence of one incident.
func (s *Service) incidentEvidence(ctx context.Context, org uuid.UUID, inc store.Incident, alerts []store.Alert) (*toolOut, error) {
	idByName := map[string]string{}
	cs, err := s.cfg.Store.ListContainers(ctx, org, inc.HostID)
	if err != nil {
		return nil, fmt.Errorf("operator: load containers: %w", err)
	}
	for _, c := range cs {
		idByName[c.Name] = c.ContainerID
	}
	incD := map[string]any{"incident_id": inc.ID.String(), "host_id": inc.HostID.String(),
		"severity": oneOf(inc.Severity, "low", "medium", "high", "critical"), "state": oneOf(inc.State, "open", "resolved"),
		"opened_at": inc.OpenedAt.UTC().Format(time.RFC3339)}
	if inc.ResolvedAt != nil {
		incD["resolved_at"] = inc.ResolvedAt.UTC().Format(time.RFC3339)
	}
	alD, alU := []map[string]any{}, []map[string]any{}
	for _, a := range alerts {
		if len(alD) >= 50 {
			break
		}
		d := map[string]any{"alert_id": a.ID.String(), "host_id": a.HostID.String(), "incident_id": a.IncidentID.String(),
			"rule": a.Rule, "severity": oneOf(a.Severity, "low", "medium", "high", "critical"),
			"state": oneOf(a.State, "firing", "resolved"), "first_at": a.FirstAt.UTC().Format(time.RFC3339),
			"last_at": a.LastAt.UTC().Format(time.RFC3339)}
		if a.HostID == inc.HostID {
			if id := idByName[a.Container]; id != "" {
				d["container_id"] = id
			}
		}
		alD = append(alD, d)
		alU = append(alU, map[string]any{"alert_id": a.ID.String(), "container": truncateBytes(a.Container, 200), "summary": truncateBytes(a.Summary, 400)})
	}
	return &toolOut{
		kind: EvidenceEvents, trusted: true, subjectType: "incident", subjectID: inc.ID.String(),
		title: "Incident and its alerts", summary: fmt.Sprintf("1 incident, %d alerts", len(alD)),
		link:      "/incidents/" + inc.ID.String(),
		data:      map[string]any{"incidents": []map[string]any{incD}, "alerts": alD, "truncated": len(alerts) > len(alD)},
		untrusted: map[string]any{"incidents": []map[string]any{{"incident_id": inc.ID.String(), "title": truncateBytes(inc.Title, 200)}}, "alerts": alU},
	}, nil
}
