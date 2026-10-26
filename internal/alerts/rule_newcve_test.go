package alerts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
)

func TestRuleNewCVE(t *testing.T) {
	host := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	cid := "ctr-abc"
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)

	tests := []struct {
		name     string
		snap     alerts.Snapshot
		wantFire bool
		wantSev  string
		wantKey  string
	}{
		{
			name: "fires on critical finding",
			snap: alerts.Snapshot{
				Now:      now,
				CVESince: now.Add(-15 * time.Second),
				CVEFindings: []alerts.CVEFindingFact{{
					HostID: host, HostName: "h1", ContainerID: cid, Name: "web",
					Image: "img:1", StartedAt: &started,
					VulnID: "CVE-2024-1", Package: "libfoo", Version: "1.0.0",
					FixedVersion: "1.0.1", Severity: store.FindingCritical,
					FirstSeen: now,
				}},
			},
			wantFire: true, wantSev: store.SeverityCritical,
			wantKey: "new_cve|" + cid + "|CVE-2024-1",
		},
		{
			name: "collapses multiple into one",
			snap: alerts.Snapshot{
				Now: now, CVESince: now.Add(-time.Minute),
				CVEFindings: []alerts.CVEFindingFact{
					{
						HostID: host, ContainerID: cid, Name: "web", Image: "img",
						VulnID: "CVE-A", Package: "a", Severity: store.FindingHigh, FirstSeen: now,
					},
					{
						HostID: host, ContainerID: cid, Name: "web", Image: "img",
						VulnID: "CVE-B", Package: "b", Severity: store.FindingCritical, FirstSeen: now,
					},
				},
			},
			wantFire: true, wantSev: store.SeverityCritical, wantKey: "new_cve|" + cid + "|CVE-B",
		},
		{
			name: "ignores medium",
			snap: alerts.Snapshot{
				CVEFindings: []alerts.CVEFindingFact{{
					HostID: host, ContainerID: cid,
					VulnID: "CVE-M", Severity: store.FindingMedium, FirstSeen: now,
				}},
			},
			wantFire: false,
		},
		{
			name:     "empty findings",
			snap:     alerts.Snapshot{},
			wantFire: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := alerts.EvaluateRules(tt.snap)
			var hit *alerts.Candidate
			for i := range got {
				if got[i].Rule == store.RuleNewCVE {
					hit = &got[i]
					break
				}
			}
			if tt.wantFire && hit == nil {
				t.Fatalf("expected new_cve fire, got %#v", got)
			}
			if !tt.wantFire && hit != nil {
				t.Fatalf("unexpected fire: %#v", hit)
			}
			if hit == nil {
				return
			}
			if hit.Severity != tt.wantSev {
				t.Errorf("severity = %s, want %s", hit.Severity, tt.wantSev)
			}
			if hit.DedupeKey != tt.wantKey {
				t.Errorf("dedupe = %s, want %s", hit.DedupeKey, tt.wantKey)
			}
			var ev struct {
				Vulns []any  `json:"vulns"`
				Image string `json:"image"`
			}
			if err := json.Unmarshal(hit.Evidence, &ev); err != nil {
				t.Fatalf("evidence: %v", err)
			}
			if len(ev.Vulns) == 0 {
				t.Error("expected vulns in evidence")
			}
		})
	}
}

func newCVECandidates(t *testing.T, snap alerts.Snapshot) map[string]alerts.Candidate {
	t.Helper()
	out := map[string]alerts.Candidate{}
	for _, c := range alerts.EvaluateRules(snap) {
		if c.Rule == store.RuleNewCVE {
			out[c.DedupeKey] = c
		}
	}
	return out
}

func TestRuleNewCVEIgnoresBacklog(t *testing.T) {
	host := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	f := alerts.CVEFindingFact{
		HostID: host, ContainerID: "c1", Name: "web", Image: "img", StartedAt: &started,
		VulnID: "CVE-OLD", Package: "p", Severity: store.FindingCritical,
	}
	// First seen long before the previous evaluation: backlog, no alert.
	old := f
	old.FirstSeen = now.Add(-48 * time.Hour)
	if got := newCVECandidates(t, alerts.Snapshot{Now: now, CVESince: now.Add(-15 * time.Second), CVEFindings: []alerts.CVEFindingFact{old}}); len(got) != 0 {
		t.Fatalf("backlog finding fired: %v", got)
	}
	// First seen after the previous evaluation but before the container
	// started: not new for this container.
	beforeStart := f
	beforeStart.FirstSeen = now.Add(-2 * time.Hour)
	if got := newCVECandidates(t, alerts.Snapshot{Now: now, CVESince: now.Add(-3 * time.Hour), CVEFindings: []alerts.CVEFindingFact{beforeStart}}); len(got) != 0 {
		t.Fatalf("finding older than container start fired: %v", got)
	}
}

func TestRuleNewCVESecondCVEGetsOwnKey(t *testing.T) {
	host := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	a := alerts.CVEFindingFact{
		HostID: host, ContainerID: "c1", Name: "web", Image: "img", StartedAt: &started,
		VulnID: "CVE-A", Package: "p", Severity: store.FindingHigh,
		FirstSeen: now.Add(-30 * time.Minute), Active: true, // alert for A is firing
	}
	b := a
	b.VulnID, b.Active, b.FirstSeen = "CVE-B", false, now.Add(-5*time.Second)
	got := newCVECandidates(t, alerts.Snapshot{Now: now, CVESince: now.Add(-15 * time.Second), CVEFindings: []alerts.CVEFindingFact{a, b}})
	if _, ok := got[alerts.NewCVEKey("c1", "CVE-B")]; !ok {
		t.Fatalf("new CVE-B must have its own dedupe key: %v", got)
	}
	if _, ok := got[alerts.NewCVEKey("c1", "CVE-A")]; !ok {
		t.Fatalf("firing alert for CVE-A must be carried forward: %v", got)
	}
	// Next evaluation: B is no longer new but its alert is firing, so both stay.
	b.Active = true
	got = newCVECandidates(t, alerts.Snapshot{Now: now.Add(time.Minute), CVESince: now, CVEFindings: []alerts.CVEFindingFact{a, b}})
	if len(got) != 2 {
		t.Fatalf("both alerts must stay open: %v", got)
	}
	// Finding gone: nothing fires, so the evaluator resolves both.
	if got := newCVECandidates(t, alerts.Snapshot{Now: now, CVESince: now}); len(got) != 0 {
		t.Fatalf("fired with no findings: %v", got)
	}
}
