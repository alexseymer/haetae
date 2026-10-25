package updates

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

// Sender delivers commands to connected agents.
type Sender interface {
	Send(ctx context.Context, hostID uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error)
}

// Store is the persistence the loop needs.
type Store interface {
	ListOrgIDs(ctx context.Context) ([]uuid.UUID, error)
	OrgAutoPull(ctx context.Context, orgID uuid.UUID) (bool, error)
	ListImageUpdatesByDecision(ctx context.Context, orgID uuid.UUID, decisions []string) ([]store.ImageUpdate, error)
}

// Loop sends PullImage / ScanImage according to policy and org auto_pull.
type Loop struct {
	Store  Store
	Sender Sender
	Lookup FindingsLookup
	Log    *slog.Logger
	Every  time.Duration

	mu      sync.Mutex
	scanned map[string]time.Time // candidate digest → last ScanImage
	scanTTL time.Duration
}

// NewLoop returns a Loop. Every defaults to 1 minute.
func NewLoop(st Store, sender Sender, log *slog.Logger) *Loop {
	if log == nil {
		log = slog.Default()
	}
	return &Loop{
		Store:   st,
		Sender:  sender,
		Log:     log,
		Every:   time.Minute,
		scanned: make(map[string]time.Time),
		scanTTL: 24 * time.Hour,
	}
}

// Run ticks until ctx ends.
func (l *Loop) Run(ctx context.Context) {
	every := l.Every
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	l.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.tick(ctx)
		}
	}
}

func (l *Loop) tick(ctx context.Context) {
	orgs, err := l.Store.ListOrgIDs(ctx)
	if err != nil {
		l.Log.Debug("updates list orgs", "err", err)
		return
	}
	for _, orgID := range orgs {
		l.tickOrg(ctx, orgID)
	}
}

func (l *Loop) tickOrg(ctx context.Context, orgID uuid.UUID) {
	auto, err := l.Store.OrgAutoPull(ctx, orgID)
	if err != nil {
		return
	}
	// Always consider wait rows that need a candidate scan.
	waiting, _ := l.Store.ListImageUpdatesByDecision(ctx, orgID, []string{DecisionWait})
	for _, u := range waiting {
		l.maybeScan(ctx, u)
	}
	if !auto {
		return
	}
	eligible, err := l.Store.ListImageUpdatesByDecision(ctx, orgID, []string{DecisionEligible, DecisionEligibleCriticalFix})
	if err != nil {
		return
	}
	for _, u := range eligible {
		l.sendPull(ctx, u)
	}
}

func (l *Loop) maybeScan(ctx context.Context, u store.ImageUpdate) {
	if l.Sender == nil || u.CandidateDigest == "" {
		return
	}
	runningFindingsID := u.RunningDigest
	if rs, ok := l.Store.(interface {
		LatestScanForContainer(context.Context, uuid.UUID, uuid.UUID, string) (store.Scan, error)
	}); ok {
		if sc, err := rs.LatestScanForContainer(ctx, u.OrgID, u.HostID, u.ContainerID); err == nil && sc.Status == store.ScanOK && sc.ImageID != "" {
			runningFindingsID = sc.ImageID
		}
	}
	in := Input{
		RunningDigest:    u.RunningDigest,
		CandidateDigest:  u.CandidateDigest,
		CandidateCreated: timeOrZero(u.CandidateCreated),
		Status:           "ok",
		Now:              time.Now(),
		OrgID:            u.OrgID.String(),
		RunningImageID:   runningFindingsID,
		Lookup:           l.Lookup,
	}
	dec := Decide(in)
	if !dec.NeedsCandidateScan {
		return
	}
	l.mu.Lock()
	last, ok := l.scanned[u.CandidateDigest]
	if ok && time.Since(last) < l.scanTTL {
		l.mu.Unlock()
		return
	}
	l.scanned[u.CandidateDigest] = time.Now()
	l.mu.Unlock()

	cmd := &agentv1.Command{Kind: &agentv1.Command_ScanImage{ScanImage: &agentv1.ScanImage{
		ScanId: uuid.NewString(),
		Ref:    u.Image,
		Digest: u.CandidateDigest,
	}}}
	if _, err := l.Sender.Send(ctx, u.HostID, cmd); err != nil {
		l.Log.Debug("scan image send", "host", u.HostID, "err", err)
	}
}

func (l *Loop) sendPull(ctx context.Context, u store.ImageUpdate) {
	if l.Sender == nil || u.CandidateDigest == "" {
		return
	}
	allowYoung := u.Decision == DecisionEligibleCriticalFix
	cmd := &agentv1.Command{Kind: &agentv1.Command_PullImage{PullImage: &agentv1.PullImage{
		Ref:         u.Image,
		Digest:      u.CandidateDigest,
		AllowYoung:  allowYoung,
		MinAgeHours: 48,
	}}}
	if _, err := l.Sender.Send(ctx, u.HostID, cmd); err != nil {
		l.Log.Debug("pull image send", "host", u.HostID, "err", err)
	}
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ApplyCandidate runs Decide and returns the ImageUpdate to store.
// findingsImageID is the SBOM image id used for RunningFindings (typically the
// container's latest ok scan image_id). When empty, RunningDigest is used.
func ApplyCandidate(orgID, hostID uuid.UUID, c *agentv1.ImageCandidate, lookup FindingsLookup, now time.Time, findingsImageID string) store.ImageUpdate {
	var created *time.Time
	if c.GetCandidateCreatedUnixMs() > 0 {
		t := time.UnixMilli(c.GetCandidateCreatedUnixMs()).UTC()
		created = &t
	}
	runningFindingsID := findingsImageID
	if runningFindingsID == "" {
		runningFindingsID = c.GetRunningDigest()
	}
	in := Input{
		RunningDigest:    c.GetRunningDigest(),
		CandidateDigest:  c.GetCandidateDigest(),
		CandidateCreated: timeOrZero(created),
		Status:           c.GetStatus(),
		Error:            c.GetError(),
		Now:              now,
		OrgID:            orgID.String(),
		RunningImageID:   runningFindingsID,
		Lookup:           lookup,
	}
	dec := Decide(in)
	return store.ImageUpdate{
		OrgID:            orgID,
		HostID:           hostID,
		ContainerID:      c.GetContainerId(),
		Image:            c.GetImage(),
		RunningDigest:    c.GetRunningDigest(),
		CandidateDigest:  c.GetCandidateDigest(),
		CandidateCreated: created,
		Decision:         dec.Decision,
		Reason:           dec.Reason,
	}
}
