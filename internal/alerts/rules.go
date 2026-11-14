package alerts

import (
	"fmt"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

const (
	crashLoopWindow   = 10 * time.Minute
	crashLoopMinCount = 3
	oomWindow         = 10 * time.Minute
	unhealthyMin      = 60 * time.Second
	diskCriticalPct   = 97.0
	deployLookback    = 15 * time.Minute
	// certExpiryWarn is how far before expiry an unrenewed client certificate
	// raises an alert. The agent starts renewing 30 days before expiry and
	// checks daily, so a healthy host renews within a day; a day less here
	// keeps a normal renewal from raising a spurious alert.
	certExpiryWarn = 29 * 24 * time.Hour
)

// EvaluateRules runs every rule over snap and returns firing candidates.
func EvaluateRules(snap Snapshot) []Candidate {
	var out []Candidate
	out = append(out, ruleCrashLoop(snap)...)
	out = append(out, ruleOOMKill(snap)...)
	out = append(out, ruleUnhealthy(snap)...)
	out = append(out, ruleHostOffline(snap)...)
	out = append(out, ruleCPUHigh(snap)...)
	out = append(out, ruleMemHigh(snap)...)
	out = append(out, ruleDiskHigh(snap)...)
	out = append(out, ruleDeployFailed(snap)...)
	out = append(out, ruleNewCVE(snap)...)
	out = append(out, ruleCertExpiring(snap)...)
	return out
}

func ruleCrashLoop(snap Snapshot) []Candidate {
	var out []Candidate
	for _, c := range snap.Containers {
		if c.RestartsInWindow < crashLoopMinCount {
			continue
		}
		name := displayName(c.Name, c.ContainerID)
		out = append(out, Candidate{
			HostID:    c.HostID,
			HostName:  c.HostName,
			Rule:      store.RuleCrashLoop,
			Container: name,
			Severity:  store.SeverityHigh,
			DedupeKey: fmt.Sprintf("%s|%s|%s", store.RuleCrashLoop, c.HostID, c.ContainerID),
			Summary:   fmt.Sprintf("%s restart loop", name),
		})
	}
	return out
}

func ruleOOMKill(snap Snapshot) []Candidate {
	var out []Candidate
	for _, c := range snap.Containers {
		if !c.OOMInWindow {
			continue
		}
		name := displayName(c.Name, c.ContainerID)
		out = append(out, Candidate{
			HostID:    c.HostID,
			HostName:  c.HostName,
			Rule:      store.RuleOOMKill,
			Container: name,
			Severity:  store.SeverityHigh,
			DedupeKey: fmt.Sprintf("%s|%s|%s", store.RuleOOMKill, c.HostID, c.ContainerID),
			Summary:   fmt.Sprintf("out of memory on %s", c.HostName),
		})
	}
	return out
}

func ruleUnhealthy(snap Snapshot) []Candidate {
	var out []Candidate
	for _, c := range snap.Containers {
		if c.UnhealthyFor < unhealthyMin {
			continue
		}
		name := displayName(c.Name, c.ContainerID)
		out = append(out, Candidate{
			HostID:    c.HostID,
			HostName:  c.HostName,
			Rule:      store.RuleUnhealthy,
			Container: name,
			Severity:  store.SeverityMedium,
			DedupeKey: fmt.Sprintf("%s|%s|%s", store.RuleUnhealthy, c.HostID, c.ContainerID),
			Summary:   fmt.Sprintf("%s unhealthy", name),
		})
	}
	return out
}

func ruleHostOffline(snap Snapshot) []Candidate {
	var out []Candidate
	for _, h := range snap.Hosts {
		if !h.Offline {
			continue
		}
		out = append(out, Candidate{
			HostID:    h.ID,
			HostName:  h.Name,
			Rule:      store.RuleHostOffline,
			Severity:  store.SeverityCritical,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleHostOffline, h.ID),
			Summary:   fmt.Sprintf("%s offline", h.Name),
		})
	}
	return out
}

func ruleCPUHigh(snap Snapshot) []Candidate {
	var out []Candidate
	for _, h := range snap.Hosts {
		if !h.CPUHigh {
			continue
		}
		out = append(out, Candidate{
			HostID:    h.ID,
			HostName:  h.Name,
			Rule:      store.RuleCPUHigh,
			Severity:  store.SeverityMedium,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleCPUHigh, h.ID),
			Summary:   fmt.Sprintf("%s CPU high", h.Name),
		})
	}
	return out
}

func ruleMemHigh(snap Snapshot) []Candidate {
	var out []Candidate
	for _, h := range snap.Hosts {
		if !h.MemHigh {
			continue
		}
		out = append(out, Candidate{
			HostID:    h.ID,
			HostName:  h.Name,
			Rule:      store.RuleMemHigh,
			Severity:  store.SeverityMedium,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleMemHigh, h.ID),
			Summary:   fmt.Sprintf("%s memory high", h.Name),
		})
	}
	return out
}

func ruleDiskHigh(snap Snapshot) []Candidate {
	var out []Candidate
	for _, h := range snap.Hosts {
		if !h.DiskHigh {
			continue
		}
		sev := store.SeverityMedium
		if h.DiskPct >= diskCriticalPct {
			sev = store.SeverityCritical
		}
		out = append(out, Candidate{
			HostID:    h.ID,
			HostName:  h.Name,
			Rule:      store.RuleDiskHigh,
			Severity:  sev,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleDiskHigh, h.ID),
			Summary:   fmt.Sprintf("%s disk high", h.Name),
		})
	}
	return out
}

func ruleDeployFailed(snap Snapshot) []Candidate {
	var out []Candidate
	for _, d := range snap.Deploys {
		out = append(out, Candidate{
			HostID:    d.HostID,
			HostName:  d.HostName,
			Rule:      store.RuleDeployFailed,
			Severity:  store.SeverityHigh,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleDeployFailed, d.ID),
			Summary:   fmt.Sprintf("deploy %s on %s", d.Status, d.HostName),
		})
	}
	return out
}

// ruleCertExpiring fires for a host whose client certificate expires within
// certExpiryWarn: renewal has not happened (agent offline, too old, or the
// server refused it), and the host will drop off at expiry. An unknown expiry
// never fires.
func ruleCertExpiring(snap Snapshot) []Candidate {
	var out []Candidate
	for _, h := range snap.Hosts {
		if h.CertExpiresAt == nil || h.CertExpiresAt.Sub(snap.Now) >= certExpiryWarn {
			continue
		}
		sev := store.SeverityMedium
		summary := fmt.Sprintf("%s client certificate expires %s", h.Name, h.CertExpiresAt.UTC().Format("2006-01-02"))
		if !h.CertExpiresAt.After(snap.Now) {
			sev = store.SeverityHigh
			summary = fmt.Sprintf("%s client certificate expired", h.Name)
		} else if h.CertExpiresAt.Sub(snap.Now) < 7*24*time.Hour {
			sev = store.SeverityHigh
		}
		out = append(out, Candidate{
			HostID:    h.ID,
			HostName:  h.Name,
			Rule:      store.RuleCertExpiring,
			Severity:  sev,
			DedupeKey: fmt.Sprintf("%s|%s", store.RuleCertExpiring, h.ID),
			Summary:   summary,
		})
	}
	return out
}

func displayName(name, id string) string {
	if name != "" {
		return name
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// IncidentTitle builds the incident title from the highest-severity alert.
// Example: "shop: out of memory on hel1-prod".
func IncidentTitle(hostName string, alerts []store.Alert) string {
	if len(alerts) == 0 {
		if hostName == "" {
			return "incident"
		}
		return hostName + ": incident"
	}
	best := alerts[0]
	for _, a := range alerts[1:] {
		if store.SeverityRank(a.Severity) > store.SeverityRank(best.Severity) {
			best = a
		} else if store.SeverityRank(a.Severity) == store.SeverityRank(best.Severity) && a.LastAt.After(best.LastAt) {
			best = a
		}
	}
	if best.Container != "" {
		return best.Container + ": " + best.Summary
	}
	if hostName != "" {
		return hostName + ": " + best.Summary
	}
	return best.Summary
}

// MaxSeverity returns the highest severity among alerts (empty → medium).
func MaxSeverity(alerts []store.Alert) string {
	best := store.SeverityMedium
	for _, a := range alerts {
		if store.SeverityRank(a.Severity) > store.SeverityRank(best) {
			best = a.Severity
		}
	}
	return best
}
