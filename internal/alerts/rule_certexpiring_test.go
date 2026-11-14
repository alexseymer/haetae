package alerts_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
)

func TestCertExpiringRule(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	host := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	day := 24 * time.Hour

	tests := []struct {
		name    string
		expires *time.Time
		want    bool
		sev     string
	}{
		{"unknown expiry does not fire", nil, false, ""},
		{"60 days left does not fire", at(60 * day), false, ""},
		{"exactly 29 days left does not fire", at(29 * day), false, ""},
		{"28 days left fires medium", at(28 * day), true, store.SeverityMedium},
		{"3 days left fires high", at(3 * day), true, store.SeverityHigh},
		{"expired fires high", at(-time.Hour), true, store.SeverityHigh},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := alerts.Snapshot{Now: now, Hosts: []alerts.HostFact{{ID: host, Name: "h1", CertExpiresAt: tc.expires}}}
			var got []alerts.Candidate
			for _, c := range alerts.EvaluateRules(snap) {
				if c.Rule == store.RuleCertExpiring {
					got = append(got, c)
				}
			}
			if (len(got) == 1) != tc.want || len(got) > 1 {
				t.Fatalf("got %d candidates, want fire=%v", len(got), tc.want)
			}
			if tc.want {
				if got[0].Severity != tc.sev || got[0].HostID != host || got[0].DedupeKey != store.RuleCertExpiring+"|"+host.String() {
					t.Fatalf("candidate = %+v", got[0])
				}
			}
		})
	}
}
