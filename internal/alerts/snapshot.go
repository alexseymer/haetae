package alerts

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// Snapshot is the plain input to pure rule functions (no DB).
type Snapshot struct {
	Now        time.Time
	Settings   store.OrgSettings
	Hosts      []HostFact
	Containers []ContainerFact
	Deploys    []DeployFact
	// CVEFindings feeds the new_cve rule (high/critical on running containers).
	CVEFindings []CVEFindingFact
	// CVESince is the previous-evaluation watermark for ranking "new" advisories.
	CVESince time.Time
}

// HostFact is one host's state for host-level rules.
type HostFact struct {
	ID       uuid.UUID
	Name     string
	Offline  bool
	CPUHigh  bool
	MemHigh  bool
	DiskHigh bool
	DiskPct  float64 // latest sample; used for disk critical (>= 97)
}

// ContainerFact is one container's state for container-level rules.
type ContainerFact struct {
	HostID           uuid.UUID
	HostName         string
	ContainerID      string
	Name             string
	RestartsInWindow int
	OOMInWindow      bool
	UnhealthyFor     time.Duration
}

// DeployFact is a recently failed deploy.
type DeployFact struct {
	ID       uuid.UUID
	HostID   uuid.UUID
	HostName string
	Status   string
	Error    string
}

// Candidate is one firing condition produced by a rule.
type Candidate struct {
	HostID    uuid.UUID
	HostName  string
	Rule      string
	Container string
	Severity  string
	DedupeKey string
	Summary   string
	Evidence  json.RawMessage // optional; empty becomes {}
}
