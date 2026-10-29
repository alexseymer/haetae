package store

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Severities for incidents and alerts, ordered low to critical.
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// SeverityRank orders severities; unknown values rank below low.
func SeverityRank(s string) int {
	switch s {
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	}
	return 0
}

// Chat channels and message kinds (match the 0007 check constraints).
const (
	ChannelWeb      = "web"
	ChannelTelegram = "telegram"
	ChannelSlack    = "slack"
	ChannelEmail    = "email"
	ChannelSignal   = "signal"

	KindIncident = "incident"
	KindBriefing = "briefing"
	KindAnswer   = "answer"
	KindText     = "text"
	KindDigest   = "digest"
	KindAction   = "action"
)

// OrgSettings holds per-organisation alert thresholds and briefing time.
type OrgSettings struct {
	OrgID         uuid.UUID `json:"orgId"`
	TimeZone      string    `json:"timeZone"`
	BriefingHour  int       `json:"briefingHour"`
	CPUPct        int       `json:"cpuPct"`
	MemPct        int       `json:"memPct"`
	DiskPct       int       `json:"diskPct"`
	SustainedS    int       `json:"sustainedS"`
	OfflineAfterS int       `json:"offlineAfterS"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Incident groups the alerts of one host (at most one open per host).
type Incident struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"orgId"`
	HostID     uuid.UUID  `json:"hostId"`
	Title      string     `json:"title"`
	Severity   string     `json:"severity"`
	State      string     `json:"state"` // open | resolved
	OpenedAt   time.Time  `json:"openedAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

// Alert is one firing or resolved rule condition inside an incident.
type Alert struct {
	ID         uuid.UUID       `json:"id"`
	OrgID      uuid.UUID       `json:"orgId"`
	HostID     uuid.UUID       `json:"hostId"`
	IncidentID uuid.UUID       `json:"incidentId"`
	Rule       string          `json:"rule"`
	Container  string          `json:"container"`
	Severity   string          `json:"severity"`
	State      string          `json:"state"` // firing | resolved
	DedupeKey  string          `json:"dedupeKey"`
	Summary    string          `json:"summary"`
	Evidence   json.RawMessage `json:"evidence"`
	FirstAt    time.Time       `json:"firstAt"`
	LastAt     time.Time       `json:"lastAt"`
	ResolvedAt *time.Time      `json:"resolvedAt,omitempty"`
}

// Mute suppresses notifications (never evaluation) for an org, optionally
// narrowed to a host and/or rule.
type Mute struct {
	ID        uuid.UUID  `json:"id"`
	OrgID     uuid.UUID  `json:"orgId"`
	HostID    *uuid.UUID `json:"hostId,omitempty"`
	Rule      string     `json:"rule"`
	Until     time.Time  `json:"until"`
	CreatedBy *uuid.UUID `json:"createdBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ChatAccount links an external chat identity to a user.
type ChatAccount struct {
	ID         uuid.UUID `json:"id"`
	OrgID      uuid.UUID `json:"orgId"`
	UserID     uuid.UUID `json:"userId"`
	Channel    string    `json:"channel"`
	ExternalID string    `json:"externalId"`
	VerifiedAt time.Time `json:"verifiedAt"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ChatAction is a button on an outgoing message.
type ChatAction struct {
	ID    string `json:"id"` // later | mute30d | ack
	Label string `json:"label"`
}

// ChatEvidence links a statement to its evidence (a path in the web UI).
type ChatEvidence struct {
	Label string `json:"label"`
	Path  string `json:"path"` // app-relative, e.g. /hosts/<id>?tab=logs
}

// ChatMeta is the structured form of an incident or digest message for the
// web client. Links are carried in ChatMessage.Evidence. Absent for other kinds.
type ChatMeta struct {
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	State    string   `json:"state"` // open | resolved
	Host     string   `json:"host,omitempty"`
	Summary  []string `json:"summary,omitempty"`
	// Sections is set on digest messages (kind "digest"): one entry per digest
	// (log summary, security digest). Their links are the message Evidence.
	Sections []ChatDigestSection `json:"sections,omitempty"`
}

// ChatDigestSection is one digest inside a digest message.
type ChatDigestSection struct {
	Title  string   `json:"title"`
	UsedAI bool     `json:"usedAI"`
	Reason string   `json:"reason,omitempty"` // why the AI was not used
	Lines  []string `json:"lines"`            // the statements, as plain text
}

// ChatMessage is one chat message, in either direction.
type ChatMessage struct {
	ID             uuid.UUID      `json:"id"`
	OrgID          uuid.UUID      `json:"orgId"`
	Channel        string         `json:"channel"`
	AccountID      *uuid.UUID     `json:"accountId,omitempty"`
	Direction      string         `json:"direction"` // in | out
	Kind           string         `json:"kind"`
	Body           string         `json:"body"`
	Actions        []ChatAction   `json:"actions"`
	Evidence       []ChatEvidence `json:"evidence"`
	Meta           *ChatMeta      `json:"meta,omitempty"`
	IncidentID     *uuid.UUID     `json:"incidentId,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	DeliveredAt    *time.Time     `json:"deliveredAt,omitempty"`
	SendStartedAt  *time.Time     `json:"sendStartedAt,omitempty"`
	SentAt         *time.Time     `json:"sentAt,omitempty"`
	Attempts       int            `json:"attempts"`
	NextAttemptAt  time.Time      `json:"nextAttemptAt"`
	DeliveryError  string         `json:"deliveryError,omitempty"`
	IdempotencyKey string         `json:"-"` // optional unique key; empty = no idempotency
}
