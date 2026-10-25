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
	fs, err := l.Store.ListFindingsForImage(ctx, org, runningImageID, 200)
	if err != nil {
		return nil, err
	}
	return toUpdateFindings(fs), nil
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
	fs, err := l.Store.ListFindingsForImage(ctx, org, candidateDigest, 200)
	if err != nil {
		return nil, false, err
	}
	return toUpdateFindings(fs), true, nil
}

func toUpdateFindings(fs []store.Finding) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		out = append(out, Finding{VulnID: f.VulnID, Severity: f.Severity})
	}
	return out
}
