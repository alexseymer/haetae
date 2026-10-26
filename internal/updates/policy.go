// Package updates decides whether a container image candidate should be
// pulled, waited on, or ignored.
package updates

import (
	"time"
)

// Decisions match the image_updates.decision check constraint.
const (
	DecisionCurrent             = "current"
	DecisionWait                = "wait"
	DecisionEligible            = "eligible"
	DecisionEligibleCriticalFix = "eligible_critical_fix"
	DecisionUnknown             = "unknown"
)

const youngWindow = 48 * time.Hour

// Finding is a minimal CVE finding used for critical-fix verification.
type Finding struct {
	VulnID   string
	Severity string
}

// FindingsLookup looks up findings for a running image and a candidate.
// The vuln worker implements this later; tests use a fake.
type FindingsLookup interface {
	// RunningFindings returns findings for the image currently running
	// (identified by running digest / image id).
	RunningFindings(orgID, runningImageID string) ([]Finding, error)
	// CandidateFindings returns findings from a ScanImage of the candidate.
	// ok is false when no scan result is available yet.
	CandidateFindings(orgID, candidateDigest string) (findings []Finding, ok bool, err error)
}

// Input is everything Decide needs.
type Input struct {
	RunningDigest    string
	CandidateDigest  string
	CandidateCreated time.Time
	// CandidateFirstSeen is when the server first saw CandidateDigest. The
	// image config "created" time is chosen by the publisher and can be
	// backdated, so the age counts from the later of the two.
	CandidateFirstSeen time.Time
	Status             string // ok, unknown, error from the agent
	Error              string
	Now                time.Time
	OrgID              string
	RunningImageID     string // for FindingsLookup; often the running digest
	Lookup             FindingsLookup
}

// Result is a decision with a short reason.
type Result struct {
	Decision string
	Reason   string
	// NeedsCandidateScan is true when the candidate is young, a critical
	// finding exists on the running image, and no candidate scan is available.
	NeedsCandidateScan bool
}

// Decide returns the update policy decision for one container.
func Decide(in Input) Result {
	if in.Status != "" && in.Status != "ok" {
		return Result{Decision: DecisionUnknown, Reason: reasonOr(in.Error, "registry status "+in.Status)}
	}
	if in.CandidateDigest == "" {
		return Result{Decision: DecisionUnknown, Reason: reasonOr(in.Error, "no candidate digest")}
	}
	if in.RunningDigest == "" {
		// Locally built or retagged image: comparing it with whatever the
		// registry serves under that name would replace a different image.
		return Result{Decision: DecisionUnknown, Reason: "running image has no registry digest"}
	}
	if in.RunningDigest == in.CandidateDigest {
		return Result{Decision: DecisionCurrent, Reason: "same digest"}
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	// Unknown age fails closed: it is treated as young.
	young := in.CandidateCreated.IsZero() || in.CandidateFirstSeen.IsZero()
	if !young {
		since := in.CandidateCreated
		if in.CandidateFirstSeen.After(since) {
			since = in.CandidateFirstSeen
		}
		young = now.Sub(since) < youngWindow
	}
	if !young {
		return Result{Decision: DecisionEligible, Reason: "candidate age >= 48h"}
	}

	// Young candidate: wait unless a critical CVE on the running image is
	// verified fixed in the candidate.
	if in.Lookup == nil {
		return Result{Decision: DecisionWait, Reason: "candidate younger than 48h"}
	}
	running, err := in.Lookup.RunningFindings(in.OrgID, in.RunningImageID)
	if err != nil {
		return Result{Decision: DecisionWait, Reason: "candidate younger than 48h (findings unavailable)"}
	}
	crit := criticalIDs(running)
	if len(crit) == 0 {
		return Result{Decision: DecisionWait, Reason: "candidate younger than 48h"}
	}
	cand, ok, err := in.Lookup.CandidateFindings(in.OrgID, in.CandidateDigest)
	if err != nil {
		return Result{Decision: DecisionWait, Reason: "candidate younger than 48h (candidate findings error)"}
	}
	if !ok {
		return Result{
			Decision:           DecisionWait,
			Reason:             "candidate younger than 48h; critical fix check pending scan",
			NeedsCandidateScan: true,
		}
	}
	candSet := make(map[string]bool, len(cand))
	for _, f := range cand {
		candSet[f.VulnID] = true
	}
	for id := range crit {
		if !candSet[id] {
			// CVE present in running, absent in candidate → fixed.
			return Result{Decision: DecisionEligibleCriticalFix, Reason: "critical CVE fixed in candidate: " + id}
		}
	}
	return Result{Decision: DecisionWait, Reason: "candidate younger than 48h; no critical fix verified"}
}

func criticalIDs(fs []Finding) map[string]bool {
	out := make(map[string]bool)
	for _, f := range fs {
		if f.Severity == "critical" && f.VulnID != "" {
			out[f.VulnID] = true
		}
	}
	return out
}

func reasonOr(err, fallback string) string {
	if err != "" {
		return err
	}
	return fallback
}
