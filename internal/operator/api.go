// Package operator is the AI operator: it answers questions, explains
// incidents, rewrites the briefing and judges log templates, always with the
// rule-based behaviour as the fallback. It never reaches hosts by itself; see
// docs/design/m8-operator.md.
package operator

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// Level is the organisation's autonomy level.
type Level string

const (
	LevelReport  Level = "report"  // answer and report only
	LevelSuggest Level = "suggest" // may propose actions; a verified admin approves
	LevelAct     Level = "act"     // may run safe actions on its own
)

// Valid reports whether l is a known level.
func (l Level) Valid() bool { return l == LevelReport || l == LevelSuggest || l == LevelAct }

// Rank orders levels: report < suggest < act. Unknown levels rank as report.
func (l Level) Rank() int {
	switch l {
	case LevelAct:
		return 2
	case LevelSuggest:
		return 1
	}
	return 0
}

// CauseNotFound is the fixed reply when no statement is backed by evidence.
const CauseNotFound = "cause not found"

// Errors every use maps to its rule-based fallback.
var (
	ErrDisabled  = errors.New("operator disabled")         // no provider configured, or the org turned it off
	ErrCapped    = errors.New("daily AI usage cap reached") // org token cap
	ErrRedaction = errors.New("redaction failed; nothing was sent")
)

// Evidence kinds. Only EvidenceLogs is untrusted (third-party text).
const (
	EvidenceHosts      = "hosts"
	EvidenceContainers = "containers"
	EvidenceMetrics    = "metrics"
	EvidenceEvents     = "events"
	EvidenceDeploys    = "deploys"
	EvidenceFindings   = "findings"
	EvidenceLogs       = "logs"
)

// Evidence is one stored tool result. ID is the stable reference the model
// and the reply use ("ev_1").
type Evidence struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Trusted     bool            `json:"trusted"`
	SubjectType string          `json:"subjectType,omitempty"`
	SubjectID   string          `json:"subjectId,omitempty"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Link        string          `json:"link"` // UI path, for example /hosts/<id>?tab=logs
}

// Statement is one claim with the evidence that supports it. Only statements
// with valid evidence survive validation.
type Statement struct {
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

// ProposedAction is what the model asked for; the server decides.
type ProposedAction struct {
	Kind        string   `json:"kind"` // "restart_container"
	ContainerID string   `json:"containerId"`
	Reason      string   `json:"reason"`
	Evidence    []string `json:"evidence"`
}

// ActionDecision is what the server did with a proposal.
type ActionDecision struct {
	Action  ProposedAction `json:"action"`
	Outcome string         `json:"outcome"` // refused | awaiting_approval | sent
	Detail  string         `json:"detail,omitempty"`
	// ApprovalID identifies the stored proposal when Outcome is
	// awaiting_approval; the chat layer turns it into an approval button.
	ApprovalID uuid.UUID `json:"approvalId,omitempty"`
}

// Answer is a validated operator reply.
type Answer struct {
	ConversationID uuid.UUID        `json:"conversationId"`
	Statements     []Statement      `json:"statements"`      // only supported statements
	Evidence       []Evidence       `json:"evidence"`        // only the evidence they cite
	CauseNotFound  bool             `json:"causeNotFound"`   // no supported statement
	DroppedCount   int              `json:"droppedCount"`    // statements removed for missing evidence
	Actions        []ActionDecision `json:"actions,omitempty"`
}

// Text renders the answer for a chat message: each statement followed by its
// evidence ids, or CauseNotFound. Links are carried separately in Evidence.
func (a Answer) Text() string { return renderAnswer(a) }

// BriefingFacts are the structured facts a briefing is built from. The
// operator may reword them but must keep every item.
type BriefingFacts struct {
	Hosts, Containers, Running int
	Items                      []string // the "needs you" items, already short
}

// TemplateQuery is one log template to judge. Templates are already redacted
// (they were built on the host from redacted lines).
type TemplateQuery struct {
	TemplateID string
	Level      int16
	Template   string
	Count      int64
}

// Verdict is the operator's judgement of one template.
type Verdict struct {
	TemplateID string
	Green      bool // true: routine, may be skipped; false: keep sending
}

// Operator is what the rest of the server depends on.
type Operator interface {
	// Available reports whether AI work can run for the org right now, and
	// if not, why, as a short phrase for a user ("not configured", "turned
	// off", "daily usage cap reached", "provider unreachable").
	Available(ctx context.Context, orgID uuid.UUID) (ok bool, reason string)
	// Ask answers a free-text question from a verified chat account or the web.
	Ask(ctx context.Context, orgID uuid.UUID, question string) (Answer, error)
	// ExplainIncident explains an open incident from evidence.
	ExplainIncident(ctx context.Context, orgID, incidentID uuid.UUID) (Answer, error)
	// RewriteBriefing rewords ruleText. It returns ruleText unchanged with a
	// nil error never; on any failure it returns an error and the caller uses
	// ruleText.
	RewriteBriefing(ctx context.Context, orgID uuid.UUID, facts BriefingFacts, ruleText string) (string, error)
	// JudgeTemplates judges templates the rule judge could not decide.
	JudgeTemplates(ctx context.Context, orgID uuid.UUID, ts []TemplateQuery) ([]Verdict, error)
	// ExecuteApproved runs an approved proposal (an approval button press by a
	// verified admin; the chat layer checked the account).
	ExecuteApproved(ctx context.Context, orgID, approvalID uuid.UUID) error
}
