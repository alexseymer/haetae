package updates

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// StoreFindings looks up vulnerability findings for image update decisions.
type StoreFindings struct {
	Store *store.Store
}

// RunningFindings returns findings for imageID (docker image id or digest).
func (l StoreFindings) RunningFindings(orgID, runningImageID string) ([]Finding, error) {
	if l.Store == nil || runningImageID == "" {
		return nil, nil
	}
	org, err := uuid.Parse(orgID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Exact, untruncated: a LIMIT could hide a present critical CVE and make it
	// look fixed in the candidate.
	ids, err := l.Store.ImageVulnIDs(ctx, org, runningImageID, true)
	if err != nil {
		return nil, err
	}
	return idsToFindings(ids, "critical"), nil
}

// CandidateFindings returns findings for a candidate digest after ScanImage.
// ok is false when no successful match exists yet.
func (l StoreFindings) CandidateFindings(orgID, candidateDigest string) ([]Finding, bool, error) {
	if l.Store == nil || candidateDigest == "" {
		return nil, false, nil
	}
	org, err := uuid.Parse(orgID)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ms, err := l.Store.GetMatchState(ctx, org, candidateDigest)
	if err != nil {
		return nil, false, err
	}
	if ms.Status != store.MatchOK {
		return nil, false, nil
	}
	ids, err := l.Store.ImageVulnIDs(ctx, org, candidateDigest, false)
	if err != nil {
		return nil, false, err
	}
	return idsToFindings(ids, ""), true, nil
}

func idsToFindings(ids []string, severity string) []Finding {
	out := make([]Finding, 0, len(ids))
	for _, id := range ids {
		out = append(out, Finding{VulnID: id, Severity: severity})
	}
	return out
}
