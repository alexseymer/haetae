package updates

import (
	"testing"
	"time"
)

type fakeLookup struct {
	running   []Finding
	candidate []Finding
	candOK    bool
}

func (f fakeLookup) RunningFindings(orgID, runningImageID string) ([]Finding, error) {
	return f.running, nil
}
func (f fakeLookup) CandidateFindings(orgID, candidateDigest string) ([]Finding, bool, error) {
	return f.candidate, f.candOK, nil
}

func TestDecideTable(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	young := now.Add(-24 * time.Hour)
	old := now.Add(-72 * time.Hour)

	cases := []struct {
		name string
		in   Input
		want string
	}{
		{
			name: "same digest",
			in:   Input{RunningDigest: "sha256:aaa", CandidateDigest: "sha256:aaa", Status: "ok", Now: now},
			want: DecisionCurrent,
		},
		{
			name: "old eligible",
			in:   Input{RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: old, CandidateFirstSeen: old, Status: "ok", Now: now},
			want: DecisionEligible,
		},
		{
			name: "created unknown is not eligible even when long seen",
			in:   Input{RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateFirstSeen: old, Status: "ok", Now: now},
			want: DecisionUnknown,
		},
		{
			name: "first seen unknown waits",
			in:   Input{RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: old, Status: "ok", Now: now},
			want: DecisionWait,
		},
		{
			name: "backdated created but first seen recently waits",
			in:   Input{RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: old, CandidateFirstSeen: young, Status: "ok", Now: now},
			want: DecisionWait,
		},
		{
			name: "future created waits",
			in:   Input{RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: now.Add(time.Hour), CandidateFirstSeen: old, Status: "ok", Now: now},
			want: DecisionWait,
		},
		{
			name: "running digest unknown",
			in:   Input{CandidateDigest: "sha256:bbb", CandidateCreated: old, CandidateFirstSeen: old, Status: "ok", Now: now},
			want: DecisionUnknown,
		},
		{
			name: "young no critical",
			in: Input{
				RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: young,
				Status: "ok", Now: now, Lookup: fakeLookup{running: nil},
			},
			want: DecisionWait,
		},
		{
			name: "young critical fix verified",
			in: Input{
				RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: young,
				Status: "ok", Now: now, RunningImageID: "img",
				Lookup: fakeLookup{
					running:   []Finding{{VulnID: "CVE-1", Severity: "critical"}},
					candidate: []Finding{}, // CVE-1 absent → fixed
					candOK:    true,
				},
			},
			want: DecisionEligibleCriticalFix,
		},
		{
			name: "young critical still present",
			in: Input{
				RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: young,
				Status: "ok", Now: now,
				Lookup: fakeLookup{
					running:   []Finding{{VulnID: "CVE-1", Severity: "critical"}},
					candidate: []Finding{{VulnID: "CVE-1", Severity: "critical"}},
					candOK:    true,
				},
			},
			want: DecisionWait,
		},
		{
			name: "unknown registry",
			in:   Input{Status: "unknown", Error: "private registry"},
			want: DecisionUnknown,
		},
		{
			name: "young needs scan",
			in: Input{
				RunningDigest: "sha256:aaa", CandidateDigest: "sha256:bbb", CandidateCreated: young,
				Status: "ok", Now: now,
				Lookup: fakeLookup{
					running: []Finding{{VulnID: "CVE-1", Severity: "critical"}},
					candOK:  false,
				},
			},
			want: DecisionWait,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in)
			if got.Decision != tc.want {
				t.Fatalf("decision=%s want %s reason=%s", got.Decision, tc.want, got.Reason)
			}
			if tc.name == "young needs scan" && !got.NeedsCandidateScan {
				t.Fatal("expected NeedsCandidateScan")
			}
		})
	}
}
