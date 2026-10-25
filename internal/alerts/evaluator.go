package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// Config tunes the evaluator loop.
type Config struct {
	Interval time.Duration // default 15s
}

// Evaluator runs rule evaluation per organisation.
type Evaluator struct {
	r   Reader
	n   Notifier
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu sync.Mutex
	// consecutiveFalse[orgID][dedupeKey] counts consecutive evaluations where
	// the condition was false while the alert was still considered active.
	// Seeded from alert_eval_state so restarts do not false-resolve.
	consecutiveFalse map[uuid.UUID]map[string]int
	// unhealthySince tracks when a container first became unhealthy.
	unhealthySince map[uuid.UUID]map[string]time.Time
	// restartSamples[orgID][host|cid] holds recent restart_count observations.
	restartSamples map[uuid.UUID]map[string][]restartSample
	// seededOrgs remembers orgs whose DB trackers have been loaded into memory.
	seededOrgs map[uuid.UUID]bool
}

type restartSample struct {
	at    time.Time
	count int
}

// NewEvaluator constructs an Evaluator. A nil Notifier becomes NopNotifier.
func NewEvaluator(r Reader, n Notifier, cfg Config, log *slog.Logger) *Evaluator {
	if n == nil {
		n = NopNotifier{}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Evaluator{
		r:                r,
		n:                n,
		cfg:              cfg,
		log:              log,
		now:              time.Now,
		consecutiveFalse: make(map[uuid.UUID]map[string]int),
		unhealthySince:   make(map[uuid.UUID]map[string]time.Time),
		restartSamples:   make(map[uuid.UUID]map[string][]restartSample),
		seededOrgs:       make(map[uuid.UUID]bool),
	}
}

// Run evaluates every Interval until ctx ends. Errors are logged, not fatal.
// Panics in one org are recovered so other orgs still evaluate.
func (e *Evaluator) Run(ctx context.Context) {
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	e.round(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.round(ctx)
		}
	}
}

// EvaluateOnce runs a single evaluation pass (tests and Run).
func (e *Evaluator) EvaluateOnce(ctx context.Context) {
	e.round(ctx)
}

func (e *Evaluator) round(ctx context.Context) {
	orgs, err := e.r.ListOrgIDs(ctx)
	if err != nil {
		e.log.Error("list orgs", "err", err)
		return
	}
	for _, orgID := range orgs {
		e.evalOrgSafe(ctx, orgID)
	}
}

func (e *Evaluator) evalOrgSafe(ctx context.Context, orgID uuid.UUID) {
	defer func() {
		if rec := recover(); rec != nil {
			e.log.Error("alerts panic", "org_id", orgID, "panic", rec)
		}
	}()
	if err := e.evalOrg(ctx, orgID); err != nil {
		e.log.Error("alerts evaluate", "org_id", orgID, "err", err)
	}
}

func (e *Evaluator) evalOrg(ctx context.Context, orgID uuid.UUID) error {
	if err := e.seedFromDB(ctx, orgID); err != nil {
		return err
	}
	now := e.now().UTC()
	snap, err := e.buildSnapshot(ctx, orgID, now)
	if err != nil {
		return err
	}
	firing := EvaluateRules(snap)
	firingKeys := make(map[string]Candidate, len(firing))
	for _, c := range firing {
		firingKeys[c.DedupeKey] = c
	}

	existing, err := e.r.ListFiringAlerts(ctx, orgID)
	if err != nil {
		return err
	}
	existingByKey := make(map[string]store.Alert, len(existing))
	for _, a := range existing {
		existingByKey[a.DedupeKey] = a
	}

	mutes, err := e.r.ActiveMutes(ctx, orgID, now)
	if err != nil {
		return err
	}

	e.mu.Lock()
	falseMap := e.consecutiveFalse[orgID]
	if falseMap == nil {
		falseMap = make(map[string]int)
		e.consecutiveFalse[orgID] = falseMap
	}
	e.mu.Unlock()

	// Resolve alerts whose condition has been false for 2 consecutive evaluations.
	for key, a := range existingByKey {
		if _, still := firingKeys[key]; still {
			e.mu.Lock()
			delete(falseMap, key)
			e.mu.Unlock()
			_ = e.r.DeleteAlertEvalState(ctx, orgID, store.EvalKindFalseStreak, key)
			continue
		}
		e.mu.Lock()
		falseMap[key]++
		n := falseMap[key]
		e.mu.Unlock()
		_ = e.r.UpsertAlertEvalState(ctx, store.AlertEvalState{
			OrgID: orgID, Kind: store.EvalKindFalseStreak, Key: key, FalseStreak: n,
		})
		if n < 2 {
			continue
		}
		if _, err := e.r.ResolveAlert(ctx, orgID, key); err != nil {
			e.log.Error("resolve alert", "org_id", orgID, "dedupe_key", key, "err", err)
			continue
		}
		e.mu.Lock()
		delete(falseMap, key)
		e.mu.Unlock()
		_ = e.r.DeleteAlertEvalState(ctx, orgID, store.EvalKindFalseStreak, key)
		if err := e.afterAlertResolved(ctx, orgID, a.IncidentID, mutes); err != nil {
			e.log.Error("after resolve", "org_id", orgID, "err", err)
		}
	}

	// Group firing candidates into per-host incidents.
	byHost := map[uuid.UUID][]Candidate{}
	for _, c := range firing {
		byHost[c.HostID] = append(byHost[c.HostID], c)
	}
	for hostID, cands := range byHost {
		if err := e.applyHost(ctx, orgID, hostID, cands, existingByKey, mutes); err != nil {
			e.log.Error("apply host alerts", "org_id", orgID, "host_id", hostID, "err", err)
		}
	}
	return nil
}

func (e *Evaluator) applyHost(
	ctx context.Context,
	orgID, hostID uuid.UUID,
	cands []Candidate,
	existingByKey map[string]store.Alert,
	mutes []store.Mute,
) error {
	hostName := ""
	if len(cands) > 0 {
		hostName = cands[0].HostName
	}
	// Seed title/severity from the highest candidate before open.
	seed := cands[0]
	for _, c := range cands[1:] {
		if store.SeverityRank(c.Severity) > store.SeverityRank(seed.Severity) {
			seed = c
		}
	}
	title := incidentTitleFromCandidate(seed)
	inc, opened, err := e.r.OpenIncidentForHost(ctx, orgID, hostID, title, seed.Severity)
	if err != nil {
		return err
	}
	prevSev := inc.Severity

	var notifyUnmuted bool
	for _, c := range cands {
		_, wasFiring := existingByKey[c.DedupeKey]
		a := store.Alert{
			OrgID:      orgID,
			HostID:     hostID,
			IncidentID: inc.ID,
			Rule:       c.Rule,
			Container:  c.Container,
			Severity:   c.Severity,
			DedupeKey:  c.DedupeKey,
			Summary:    c.Summary,
			Evidence:   json.RawMessage(`{}`),
		}
		_, created, err := e.r.UpsertFiringAlert(ctx, a)
		if err != nil {
			return err
		}
		// Notify only when INSERT says newly created (xmax=0), or later on
		// severity escalation. Concurrent evaluators must not both fan out.
		_ = wasFiring
		if created && !ruleMuted(mutes, hostID, c.Rule) {
			notifyUnmuted = true
		}
	}

	_, allAlerts, err := e.r.GetIncident(ctx, orgID, inc.ID)
	if err != nil {
		return err
	}
	var firingOnly []store.Alert
	for _, a := range allAlerts {
		if a.State == store.AlertFiring {
			firingOnly = append(firingOnly, a)
		}
	}
	newTitle := IncidentTitle(hostName, firingOnly)
	newSev := MaxSeverity(firingOnly)
	escalated := store.SeverityRank(newSev) > store.SeverityRank(prevSev)
	if newTitle != inc.Title || newSev != inc.Severity {
		inc, err = e.r.UpdateIncident(ctx, orgID, inc.ID, newTitle, newSev)
		if err != nil {
			return err
		}
	}
	if escalated && !allFiringMuted(mutes, hostID, firingOnly) {
		notifyUnmuted = true
	}
	_ = opened // incident open without a new alert/escalation does not notify
	if !notifyUnmuted {
		return nil
	}
	return e.n.IncidentChanged(ctx, orgID, inc, firingOnly)
}

func (e *Evaluator) afterAlertResolved(ctx context.Context, orgID, incidentID uuid.UUID, mutes []store.Mute) error {
	inc, alerts, err := e.r.GetIncident(ctx, orgID, incidentID)
	if err != nil {
		return err
	}
	var firing []store.Alert
	for _, a := range alerts {
		if a.State == store.AlertFiring {
			firing = append(firing, a)
		}
	}
	if len(firing) > 0 {
		title := IncidentTitle("", firing)
		sev := MaxSeverity(firing)
		if title != inc.Title || sev != inc.Severity {
			inc, err = e.r.UpdateIncident(ctx, orgID, incidentID, title, sev)
			if err != nil {
				return err
			}
			if !allFiringMuted(mutes, inc.HostID, firing) {
				_ = e.n.IncidentChanged(ctx, orgID, inc, firing)
			}
		}
		return nil
	}
	inc, err = e.r.ResolveIncident(ctx, orgID, incidentID)
	if err != nil {
		return err
	}
	if allFiringMuted(mutes, inc.HostID, alerts) {
		return nil
	}
	return e.n.IncidentChanged(ctx, orgID, inc, alerts)
}

func ruleMuted(mutes []store.Mute, hostID uuid.UUID, rule string) bool {
	for _, m := range mutes {
		if store.MuteMatches(m, hostID, rule) {
			return true
		}
	}
	return false
}

func allFiringMuted(mutes []store.Mute, hostID uuid.UUID, alerts []store.Alert) bool {
	if len(alerts) == 0 {
		return ruleMuted(mutes, hostID, "")
	}
	for _, a := range alerts {
		if !ruleMuted(mutes, hostID, a.Rule) {
			return false
		}
	}
	return true
}

func incidentTitleFromCandidate(c Candidate) string {
	if c.Container != "" {
		return c.Container + ": " + c.Summary
	}
	if c.HostName != "" {
		return c.HostName + ": " + c.Summary
	}
	return c.Summary
}

func (e *Evaluator) buildSnapshot(ctx context.Context, orgID uuid.UUID, now time.Time) (Snapshot, error) {
	settings, err := e.r.GetOrgSettings(ctx, orgID)
	if err != nil {
		return Snapshot{}, err
	}
	hosts, err := e.r.ListHosts(ctx, orgID)
	if err != nil {
		return Snapshot{}, err
	}
	hostNames := make(map[uuid.UUID]string, len(hosts))
	for _, h := range hosts {
		hostNames[h.ID] = h.Name
	}

	cutoff := now.Add(-time.Duration(settings.OfflineAfterS) * time.Second)
	offline, err := e.r.HostsOfflineSince(ctx, orgID, cutoff)
	if err != nil {
		return Snapshot{}, err
	}
	offlineSet := make(map[uuid.UUID]bool, len(offline))
	for _, h := range offline {
		offlineSet[h.ID] = true
	}

	sustained := time.Duration(settings.SustainedS) * time.Second
	hostFacts := make([]HostFact, 0, len(hosts))
	for _, h := range hosts {
		if h.RevokedAt != nil {
			continue
		}
		hf := HostFact{ID: h.ID, Name: h.Name, Offline: offlineSet[h.ID]}
		cpu, err := e.r.HostMetricSustained(ctx, orgID, h.ID, "cpu_pct", float64(settings.CPUPct), sustained, now)
		if err != nil {
			return Snapshot{}, err
		}
		mem, err := e.r.HostMetricSustained(ctx, orgID, h.ID, "mem_pct", float64(settings.MemPct), sustained, now)
		if err != nil {
			return Snapshot{}, err
		}
		disk, err := e.r.HostMetricSustained(ctx, orgID, h.ID, "disk_pct", float64(settings.DiskPct), sustained, now)
		if err != nil {
			return Snapshot{}, err
		}
		pct, err := e.r.HostDiskPct(ctx, orgID, h.ID)
		if err != nil {
			return Snapshot{}, err
		}
		hf.CPUHigh, hf.MemHigh, hf.DiskHigh, hf.DiskPct = cpu, mem, disk, pct
		hostFacts = append(hostFacts, hf)
	}

	restarts, err := e.r.ContainerRestarts(ctx, orgID)
	if err != nil {
		return Snapshot{}, err
	}
	oom, err := e.r.ContainersOOMSince(ctx, orgID, now.Add(-oomWindow))
	if err != nil {
		return Snapshot{}, err
	}
	oomSet := make(map[string]bool, len(oom))
	for _, o := range oom {
		oomSet[o.HostID.String()+"|"+o.ContainerID] = true
	}
	health, err := e.r.ContainerHealth(ctx, orgID)
	if err != nil {
		return Snapshot{}, err
	}
	healthByKey := make(map[string]store.ContainerHealthFact, len(health))
	for _, h := range health {
		healthByKey[h.HostID.String()+"|"+h.ContainerID] = h
	}

	e.mu.Lock()
	rsMap := e.restartSamples[orgID]
	if rsMap == nil {
		rsMap = make(map[string][]restartSample)
		e.restartSamples[orgID] = rsMap
	}
	uhMap := e.unhealthySince[orgID]
	if uhMap == nil {
		uhMap = make(map[string]time.Time)
		e.unhealthySince[orgID] = uhMap
	}
	e.mu.Unlock()

	seenKeys := make(map[string]bool, len(restarts))
	var containers []ContainerFact
	for _, r := range restarts {
		key := r.HostID.String() + "|" + r.ContainerID
		seenKeys[key] = true
		e.mu.Lock()
		samples := append(rsMap[key], restartSample{at: now, count: r.RestartCount})
		cut := now.Add(-crashLoopWindow)
		i := 0
		for i < len(samples) && samples[i].at.Before(cut) {
			i++
		}
		samples = samples[i:]
		rsMap[key] = samples
		delta := 0
		var restartPersist *store.AlertEvalState
		if len(samples) > 0 {
			minC, maxC := samples[0].count, samples[0].count
			for _, s := range samples[1:] {
				if s.count < minC {
					minC = s.count
				}
				if s.count > maxC {
					maxC = s.count
				}
			}
			delta = maxC - minC
			// A single observation with count already high (e.g. after restart
			// of the evaluator) still counts the current restart_count when
			// started recently inside the window.
			if delta == 0 && r.RestartCount >= crashLoopMinCount && r.StartedAt != nil && !r.StartedAt.Before(cut) {
				delta = r.RestartCount
			}
			baseline := minC
			at0 := samples[0].at
			restartPersist = &store.AlertEvalState{
				OrgID: orgID, Kind: store.EvalKindRestart, Key: key, RestartBaseline: &baseline,
				FirstSeen: &at0,
			}
		}
		e.mu.Unlock()
		if restartPersist != nil {
			_ = e.r.UpsertAlertEvalState(ctx, *restartPersist)
		}

		var unhealthyFor time.Duration
		hf, ok := healthByKey[key]
		var uhPersist *store.AlertEvalState
		var uhDelete bool
		e.mu.Lock()
		if ok && hf.Health == "unhealthy" {
			since, exists := uhMap[key]
			if !exists {
				uhMap[key] = now
				since = now
				uhPersist = &store.AlertEvalState{
					OrgID: orgID, Kind: store.EvalKindUnhealthy, Key: key, FirstSeen: &now,
				}
			}
			unhealthyFor = now.Sub(since)
		} else {
			if _, existed := uhMap[key]; existed {
				uhDelete = true
			}
			delete(uhMap, key)
		}
		e.mu.Unlock()
		if uhPersist != nil {
			_ = e.r.UpsertAlertEvalState(ctx, *uhPersist)
		}
		if uhDelete {
			_ = e.r.DeleteAlertEvalState(ctx, orgID, store.EvalKindUnhealthy, key)
		}

		containers = append(containers, ContainerFact{
			HostID:           r.HostID,
			HostName:         hostNames[r.HostID],
			ContainerID:      r.ContainerID,
			Name:             r.Name,
			RestartsInWindow: delta,
			OOMInWindow:      oomSet[key],
			UnhealthyFor:     unhealthyFor,
		})
	}
	// Drop tracker entries for removed containers.
	e.mu.Lock()
	for k := range rsMap {
		if !seenKeys[k] {
			delete(rsMap, k)
		}
	}
	for k := range uhMap {
		if !seenKeys[k] {
			delete(uhMap, k)
		}
	}
	e.mu.Unlock()

	deploys, err := e.r.RecentFailedDeploys(ctx, orgID, now.Add(-deployLookback))
	if err != nil {
		return Snapshot{}, err
	}
	var deployFacts []DeployFact
	for _, d := range deploys {
		deployFacts = append(deployFacts, DeployFact{
			ID:       d.ID,
			HostID:   d.HostID,
			HostName: hostNames[d.HostID],
			Status:   d.Status,
			Error:    d.Error,
		})
	}

	return Snapshot{
		Now:        now,
		Settings:   settings,
		Hosts:      hostFacts,
		Containers: containers,
		Deploys:    deployFacts,
	}, nil
}

func (e *Evaluator) seedFromDB(ctx context.Context, orgID uuid.UUID) error {
	e.mu.Lock()
	if e.seededOrgs[orgID] {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	rows, err := e.r.LoadAlertEvalState(ctx, orgID)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seededOrgs[orgID] {
		return nil
	}
	falseMap := e.consecutiveFalse[orgID]
	if falseMap == nil {
		falseMap = make(map[string]int)
		e.consecutiveFalse[orgID] = falseMap
	}
	uhMap := e.unhealthySince[orgID]
	if uhMap == nil {
		uhMap = make(map[string]time.Time)
		e.unhealthySince[orgID] = uhMap
	}
	rsMap := e.restartSamples[orgID]
	if rsMap == nil {
		rsMap = make(map[string][]restartSample)
		e.restartSamples[orgID] = rsMap
	}
	for _, st := range rows {
		switch st.Kind {
		case store.EvalKindFalseStreak:
			falseMap[st.Key] = st.FalseStreak
		case store.EvalKindUnhealthy:
			if st.FirstSeen != nil {
				uhMap[st.Key] = st.FirstSeen.UTC()
			}
		case store.EvalKindRestart:
			if st.RestartBaseline != nil {
				at := e.now().UTC()
				if st.FirstSeen != nil {
					at = st.FirstSeen.UTC()
				}
				rsMap[st.Key] = []restartSample{{at: at, count: *st.RestartBaseline}}
			}
		}
	}
	e.seededOrgs[orgID] = true
	return nil
}

// FalseCount returns the consecutive-false counter for tests.
func (e *Evaluator) FalseCount(orgID uuid.UUID, dedupeKey string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.consecutiveFalse[orgID] == nil {
		return 0
	}
	return e.consecutiveFalse[orgID][dedupeKey]
}

// SetNowFunc overrides the clock (tests).
func (e *Evaluator) SetNowFunc(f func() time.Time) {
	if f != nil {
		e.now = f
	}
}

// ApplyCandidates is a test helper that runs grouping/notify for a pre-built
// candidate set without building a snapshot from the Reader.
func (e *Evaluator) ApplyCandidates(ctx context.Context, orgID uuid.UUID, firing []Candidate, mutes []store.Mute) error {
	if err := e.seedFromDB(ctx, orgID); err != nil {
		return err
	}
	existing, err := e.r.ListFiringAlerts(ctx, orgID)
	if err != nil {
		return err
	}
	existingByKey := make(map[string]store.Alert, len(existing))
	for _, a := range existing {
		existingByKey[a.DedupeKey] = a
	}
	firingKeys := make(map[string]Candidate, len(firing))
	for _, c := range firing {
		firingKeys[c.DedupeKey] = c
	}

	e.mu.Lock()
	falseMap := e.consecutiveFalse[orgID]
	if falseMap == nil {
		falseMap = make(map[string]int)
		e.consecutiveFalse[orgID] = falseMap
	}
	e.mu.Unlock()

	for key, a := range existingByKey {
		if _, still := firingKeys[key]; still {
			e.mu.Lock()
			delete(falseMap, key)
			e.mu.Unlock()
			continue
		}
		e.mu.Lock()
		falseMap[key]++
		n := falseMap[key]
		e.mu.Unlock()
		if n < 2 {
			continue
		}
		if _, err := e.r.ResolveAlert(ctx, orgID, key); err != nil {
			return err
		}
		e.mu.Lock()
		delete(falseMap, key)
		e.mu.Unlock()
		if err := e.afterAlertResolved(ctx, orgID, a.IncidentID, mutes); err != nil {
			return err
		}
	}

	byHost := map[uuid.UUID][]Candidate{}
	for _, c := range firing {
		byHost[c.HostID] = append(byHost[c.HostID], c)
	}
	for hostID, cands := range byHost {
		if err := e.applyHost(ctx, orgID, hostID, cands, existingByKey, mutes); err != nil {
			return fmt.Errorf("host %s: %w", hostID, err)
		}
	}
	return nil
}
