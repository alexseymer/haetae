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
}

// ruleNewCVE collapses high/critical findings into one candidate per container.
// Dedupe key is new_cve|<container>. Fires while any high/critical finding
// remains on the running image; resolves when none remain (evaluator false-streak).
// Works with no AI.
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
		findings []CVEFindingFact
	}
	byContainer := map[string]*group{}
	order := []string{}
	for _, f := range snap.CVEFindings {
		if f.Severity != store.FindingHigh && f.Severity != store.FindingCritical {
			continue
		}
		key := f.ContainerID
		g, ok := byContainer[key]
		if !ok {
			g = &group{
				hostID: f.HostID, hostName: f.HostName, cid: f.ContainerID,
				name: f.Name, image: f.Image,
			}
			byContainer[key] = g
			order = append(order, key)
		}
		g.findings = append(g.findings, f)
	}

	var out []Candidate
	for _, key := range order {
		g := byContainer[key]
		sort.SliceStable(g.findings, func(i, j int) bool {
			// Prefer newest-since-watermark, then severity, then id.
			ni := isNewFinding(g.findings[i], snap.CVESince)
			nj := isNewFinding(g.findings[j], snap.CVESince)
			if ni != nj {
				return ni && !nj
			}
			ri := store.SeverityRank(mapFindingSev(g.findings[i].Severity))
			rj := store.SeverityRank(mapFindingSev(g.findings[j].Severity))
			if ri != rj {
				return ri > rj
			}
			return g.findings[i].VulnID < g.findings[j].VulnID
		})
		top := g.findings[0]
		sev := mapFindingSev(top.Severity)
		name := displayName(g.name, g.cid)
		summary := newCVESummary(top, len(g.findings))
		evidence, _ := json.Marshal(newCVEEvidence(g.image, g.findings))
		out = append(out, Candidate{
			HostID:    g.hostID,
			HostName:  g.hostName,
			Rule:      store.RuleNewCVE,
			Container: name,
			Severity:  sev,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleNewCVE, g.cid),
			Summary:   summary,
			Evidence:  evidence,
		})
	}
	return out
}

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
