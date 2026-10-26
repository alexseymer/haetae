package alerts

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// CVEFindingFact is one high/critical finding on a running container for new_cve.
type CVEFindingFact struct {
	HostID       uuid.UUID
	HostName     string
	ContainerID  string
	Name         string
	Image        string
	ImageID      string
	StartedAt    *time.Time
	VulnID       string
	Package      string
	Version      string
	FixedVersion string
	Severity     string
	FirstSeen    time.Time
	// Active is set by the evaluator when an alert for this container and CVE
	// is currently firing (so it stays open while the finding persists).
	Active bool
}

// NewCVEKey is the dedupe key for one CVE on one container.
func NewCVEKey(containerID, vulnID string) string {
	return fmt.Sprintf("%s|%s|%s", store.RuleNewCVE, containerID, vulnID)
}

// ruleNewCVE emits, per container, one candidate for the findings first seen
// after max(container start, previous evaluation) (snap.CVESince), keyed by the
// top such CVE (new_cve|<container>|<vuln>) with the count of new findings in
// the summary. A later new CVE therefore gets its own key and notification.
// Findings whose alert is already firing (Active) are carried forward under
// their own key so the alert stays open until the finding is gone. Findings
// that are merely old backlog never fire. Works with no AI.
func ruleNewCVE(snap Snapshot) []Candidate {
	if len(snap.CVEFindings) == 0 {
		return nil
	}
	type group struct {
		hostID   uuid.UUID
		hostName string
		cid      string
		name     string
		image    string
		fresh    []CVEFindingFact
		active   []CVEFindingFact
	}
	byContainer := map[string]*group{}
	order := []string{}
	for _, f := range snap.CVEFindings {
		if f.Severity != store.FindingHigh && f.Severity != store.FindingCritical {
			continue
		}
		// A finding with a firing alert is not new again (the watermark
		// trails the evaluation, so it can fall in the window twice).
		fresh := !f.Active && isNewFinding(f, snap.CVESince)
		if !fresh && !f.Active {
			continue
		}
		g, ok := byContainer[f.ContainerID]
		if !ok {
			g = &group{
				hostID: f.HostID, hostName: f.HostName, cid: f.ContainerID,
				name: f.Name, image: f.Image,
			}
			byContainer[f.ContainerID] = g
			order = append(order, f.ContainerID)
		}
		if fresh {
			g.fresh = append(g.fresh, f)
		}
		if f.Active {
			g.active = append(g.active, f)
		}
	}

	less := func(fs []CVEFindingFact) func(i, j int) bool {
		return func(i, j int) bool {
			ri := store.SeverityRank(mapFindingSev(fs[i].Severity))
			rj := store.SeverityRank(mapFindingSev(fs[j].Severity))
			if ri != rj {
				return ri > rj
			}
			return fs[i].VulnID < fs[j].VulnID
		}
	}
	var out []Candidate
	for _, key := range order {
		g := byContainer[key]
		name := displayName(g.name, g.cid)
		emitted := map[string]bool{}
		emit := func(top CVEFindingFact, n int, evidenceSet []CVEFindingFact) {
			k := NewCVEKey(g.cid, top.VulnID)
			if emitted[k] {
				return
			}
			emitted[k] = true
			ev, _ := json.Marshal(newCVEEvidence(g.image, evidenceSet))
			out = append(out, Candidate{
				HostID:    g.hostID,
				HostName:  g.hostName,
				Rule:      store.RuleNewCVE,
				Container: name,
				Severity:  mapFindingSev(top.Severity),
				DedupeKey: k,
				Summary:   newCVESummary(top, n),
				Evidence:  ev,
			})
		}
		if len(g.fresh) > 0 {
			sort.SliceStable(g.fresh, less(g.fresh))
			emit(g.fresh[0], len(g.fresh), g.fresh)
		}
		sort.SliceStable(g.active, less(g.active))
		for _, f := range g.active {
			emit(f, 1, []CVEFindingFact{f})
		}
	}
	return out
}

// isNewFinding reports whether f was first seen after the later of the
// container start and the previous evaluation watermark.
func isNewFinding(f CVEFindingFact, since time.Time) bool {
	cut := since
	if f.StartedAt != nil && f.StartedAt.After(cut) {
		cut = f.StartedAt.UTC()
	}
	return !f.FirstSeen.Before(cut)
}

func mapFindingSev(s string) string {
	switch strings.ToLower(s) {
	case store.FindingCritical:
		return store.SeverityCritical
	case store.FindingHigh:
		return store.SeverityHigh
	default:
		return store.SeverityMedium
	}
}

func newCVESummary(top CVEFindingFact, n int) string {
	fix := top.FixedVersion
	if fix == "" {
		fix = "no fix"
	}
	if n == 1 {
		return fmt.Sprintf("%s on %s (fix %s)", top.VulnID, top.Package, fix)
	}
	return fmt.Sprintf("%s and %d more on %s (fix %s)", top.VulnID, n-1, top.Package, fix)
}

type cveEvidenceVuln struct {
	ID           string `json:"id"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	FixedVersion string `json:"fixed_version,omitempty"`
	Severity     string `json:"severity"`
}

type cveEvidence struct {
	Vulns []cveEvidenceVuln `json:"vulns"`
	Image string            `json:"image"`
}

func newCVEEvidence(image string, findings []CVEFindingFact) cveEvidence {
	const maxV = 5
	ev := cveEvidence{Image: image, Vulns: make([]cveEvidenceVuln, 0, min(len(findings), maxV))}
	for i, f := range findings {
		if i >= maxV {
			break
		}
		ev.Vulns = append(ev.Vulns, cveEvidenceVuln{
			ID: f.VulnID, Package: f.Package, Version: f.Version,
			FixedVersion: f.FixedVersion, Severity: f.Severity,
		})
	}
	return ev
}
