package alerts_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
)

func TestRulesTable(t *testing.T) {
	host := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	cid := "abc123"

	tests := []struct {
		name     string
		snap     alerts.Snapshot
		wantRule string
		wantSev  string
		wantFire bool
	}{
		{
			name: "crash_loop fires",
			snap: alerts.Snapshot{Containers: []alerts.ContainerFact{{
				HostID: host, HostName: "h1", ContainerID: cid, Name: "web",
				RestartsInWindow: 3,
			}}},
			wantRule: store.RuleCrashLoop, wantSev: store.SeverityHigh, wantFire: true,
		},
		{
			name: "crash_loop does not fire below 3",
			snap: alerts.Snapshot{Containers: []alerts.ContainerFact{{
				HostID: host, ContainerID: cid, RestartsInWindow: 2,
			}}},
			wantRule: store.RuleCrashLoop, wantFire: false,
		},
		{
			name: "oom_kill fires",
			snap: alerts.Snapshot{Containers: []alerts.ContainerFact{{
				HostID: host, HostName: "hel1-prod", ContainerID: cid, Name: "shop",
				OOMInWindow: true,
			}}},
			wantRule: store.RuleOOMKill, wantSev: store.SeverityHigh, wantFire: true,
		},
		{
			name: "oom_kill does not fire",
			snap: alerts.Snapshot{Containers: []alerts.ContainerFact{{
				HostID: host, ContainerID: cid, OOMInWindow: false,
			}}},
			wantRule: store.RuleOOMKill, wantFire: false,
		},
		{
			name: "unhealthy fires at 60s",
			snap: alerts.Snapshot{Containers: []alerts.ContainerFact{{
				HostID: host, ContainerID: cid, Name: "api", UnhealthyFor: 60 * time.Second,
			}}},
			wantRule: store.RuleUnhealthy, wantSev: store.SeverityMedium, wantFire: true,
		},
		{
			name: "unhealthy does not fire under 60s",
			snap: alerts.Snapshot{Containers: []alerts.ContainerFact{{
				HostID: host, ContainerID: cid, UnhealthyFor: 59 * time.Second,
			}}},
			wantRule: store.RuleUnhealthy, wantFire: false,
		},
		{
			name: "host_offline fires",
			snap: alerts.Snapshot{Hosts: []alerts.HostFact{{
				ID: host, Name: "hel1", Offline: true,
			}}},
			wantRule: store.RuleHostOffline, wantSev: store.SeverityCritical, wantFire: true,
		},
		{
			name: "host_offline does not fire when online",
			snap: alerts.Snapshot{Hosts: []alerts.HostFact{{
				ID: host, Name: "hel1", Offline: false,
			}}},
			wantRule: store.RuleHostOffline, wantFire: false,
		},
		{
			name: "cpu_high fires",
			snap: alerts.Snapshot{Hosts: []alerts.HostFact{{
				ID: host, Name: "h1", CPUHigh: true,
			}}},
			wantRule: store.RuleCPUHigh, wantSev: store.SeverityMedium, wantFire: true,
		},
		{
			name: "mem_high fires",
			snap: alerts.Snapshot{Hosts: []alerts.HostFact{{
				ID: host, Name: "h1", MemHigh: true,
			}}},
			wantRule: store.RuleMemHigh, wantSev: store.SeverityMedium, wantFire: true,
		},
		{
			name: "disk_high medium",
			snap: alerts.Snapshot{Hosts: []alerts.HostFact{{
				ID: host, Name: "h1", DiskHigh: true, DiskPct: 91,
			}}},
			wantRule: store.RuleDiskHigh, wantSev: store.SeverityMedium, wantFire: true,
		},
		{
			name: "disk_high critical at 97",
			snap: alerts.Snapshot{Hosts: []alerts.HostFact{{
				ID: host, Name: "h1", DiskHigh: true, DiskPct: 97,
			}}},
			wantRule: store.RuleDiskHigh, wantSev: store.SeverityCritical, wantFire: true,
		},
		{
			name: "deploy_failed fires",
			snap: alerts.Snapshot{Deploys: []alerts.DeployFact{{
				ID:     uuid.MustParse("22222222-2222-2222-2222-222222222222"),
				HostID: host, HostName: "h1", Status: store.DeployFailed,
			}}},
			wantRule: store.RuleDeployFailed, wantSev: store.SeverityHigh, wantFire: true,
		},
		{
			name:     "empty snapshot fires nothing",
			snap:     alerts.Snapshot{},
			wantRule: store.RuleCrashLoop, wantFire: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := alerts.EvaluateRules(tt.snap)
			var found *alerts.Candidate
			for i := range got {
				if got[i].Rule == tt.wantRule {
					found = &got[i]
					break
				}
			}
			if tt.wantFire {
				if found == nil {
					t.Fatalf("want rule %s to fire, got %#v", tt.wantRule, got)
				}
				if found.Severity != tt.wantSev {
					t.Fatalf("severity = %s, want %s", found.Severity, tt.wantSev)
				}
				if found.DedupeKey == "" {
					t.Fatal("empty dedupe key")
				}
			} else if found != nil {
				t.Fatalf("rule %s fired unexpectedly: %+v", tt.wantRule, found)
			}
		})
	}
}

func TestIncidentTitle(t *testing.T) {
	title := alerts.IncidentTitle("hel1-prod", []store.Alert{{
		Container: "shop",
		Summary:   "out of memory on hel1-prod",
		Severity:  store.SeverityHigh,
	}})
	if title != "shop: out of memory on hel1-prod" {
		t.Fatalf("title = %q", title)
	}
}
