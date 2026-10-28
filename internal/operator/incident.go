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

	// The seed looks to the model like the result of a list_events call, so
	// the usual rule applies: third-party text only as "untrusted" values.
	user, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleUser, Text: incidentPrompt})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	call, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: "seed_0", Name: "list_events", Args: json.RawMessage(fmt.Sprintf(`{"from":%q,"to":%q}`,
			inc.OpenedAt.UTC().Format(time.RFC3339), s.cfg.Now().UTC().Format(time.RFC3339)))},
	}})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	res, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleTool, ToolCallID: "seed_0", Text: result})
	if err != nil {
		return Answer{ConversationID: c.id}, err
	}
	return s.runConv(ctx, c, []provider.Message{user, call, res}, inc.State == "open")
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
