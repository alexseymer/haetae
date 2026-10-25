package alerts_test

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// memStore is an in-memory Reader for evaluator unit tests.
type memStore struct {
	mu sync.Mutex

	orgs       []uuid.UUID
	settings   map[uuid.UUID]store.OrgSettings
	hosts      map[uuid.UUID][]store.Host
	incidents  map[uuid.UUID]*store.Incident // by id
	openByHost map[uuid.UUID]uuid.UUID       // hostID -> incident id
	alerts     map[string]*store.Alert       // org|dedupe firing
	allAlerts  []*store.Alert
	mutes      []store.Mute

	// snapshot inputs
	restarts  []store.ContainerRestartFact
	oom       []store.ContainerOOMFact
	health    []store.ContainerHealthFact
	sustained map[string]bool // host|metric
	diskPct   map[uuid.UUID]float64
	offline   []store.Host
	deploys   []store.Deploy

	evalState map[string]store.AlertEvalState // org|kind|key
}

func newMemStore(orgID uuid.UUID) *memStore {
	return &memStore{
		orgs:       []uuid.UUID{orgID},
		settings:   map[uuid.UUID]store.OrgSettings{orgID: store.DefaultOrgSettings(orgID)},
		hosts:      map[uuid.UUID][]store.Host{},
		incidents:  map[uuid.UUID]*store.Incident{},
		openByHost: map[uuid.UUID]uuid.UUID{},
		alerts:     map[string]*store.Alert{},
		sustained:  map[string]bool{},
		diskPct:    map[uuid.UUID]float64{},
		evalState:  map[string]store.AlertEvalState{},
	}
}

func (m *memStore) key(orgID uuid.UUID, dedupe string) string {
	return orgID.String() + "|" + dedupe
}

func (m *memStore) ListOrgIDs(context.Context) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]uuid.UUID{}, m.orgs...)
	return out, nil
}

func (m *memStore) GetOrgSettings(_ context.Context, orgID uuid.UUID) (store.OrgSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.settings[orgID]; ok {
		return s, nil
	}
	return store.DefaultOrgSettings(orgID), nil
}

func (m *memStore) ListHosts(_ context.Context, orgID uuid.UUID) ([]store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.Host{}, m.hosts[orgID]...), nil
}

func (m *memStore) ContainerRestarts(context.Context, uuid.UUID) ([]store.ContainerRestartFact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.ContainerRestartFact{}, m.restarts...), nil
}

func (m *memStore) ContainersOOMSince(context.Context, uuid.UUID, time.Time) ([]store.ContainerOOMFact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.ContainerOOMFact{}, m.oom...), nil
}

func (m *memStore) ContainerHealth(context.Context, uuid.UUID) ([]store.ContainerHealthFact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.ContainerHealthFact{}, m.health...), nil
}

func (m *memStore) HostMetricSustained(_ context.Context, _, hostID uuid.UUID, metric string, _ float64, _ time.Duration, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sustained[hostID.String()+"|"+metric], nil
}

func (m *memStore) HostDiskPct(_ context.Context, _, hostID uuid.UUID) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.diskPct[hostID], nil
}

func (m *memStore) HostsOfflineSince(context.Context, uuid.UUID, time.Time) ([]store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.Host{}, m.offline...), nil
}

func (m *memStore) RecentFailedDeploys(context.Context, uuid.UUID, time.Time) ([]store.Deploy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.Deploy{}, m.deploys...), nil
}

func (m *memStore) ActiveMutes(_ context.Context, orgID uuid.UUID, now time.Time) ([]store.Mute, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Mute
	for _, x := range m.mutes {
		if x.OrgID == orgID && x.Until.After(now) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (m *memStore) OpenIncidentForHost(_ context.Context, orgID, hostID uuid.UUID, title, severity string) (store.Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.openByHost[hostID]; ok {
		return *m.incidents[id], false, nil
	}
	id := uuid.New()
	inc := &store.Incident{
		ID: id, OrgID: orgID, HostID: hostID, Title: title, Severity: severity,
		State: store.IncidentOpen, OpenedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	m.incidents[id] = inc
	m.openByHost[hostID] = id
	return *inc, true, nil
}

func (m *memStore) UpdateIncident(_ context.Context, orgID, incidentID uuid.UUID, title, severity string) (store.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, ok := m.incidents[incidentID]
	if !ok || inc.OrgID != orgID {
		return store.Incident{}, store.ErrNotFound
	}
	inc.Title, inc.Severity = title, severity
	inc.UpdatedAt = time.Now().UTC()
	return *inc, nil
}

func (m *memStore) ResolveIncident(_ context.Context, orgID, incidentID uuid.UUID) (store.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, ok := m.incidents[incidentID]
	if !ok || inc.OrgID != orgID || inc.State != store.IncidentOpen {
		return store.Incident{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	inc.State = store.IncidentResolved
	inc.ResolvedAt = &now
	inc.UpdatedAt = now
	delete(m.openByHost, inc.HostID)
	return *inc, nil
}

func (m *memStore) UpsertFiringAlert(_ context.Context, a store.Alert) (store.Alert, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(a.OrgID, a.DedupeKey)
	if ex, ok := m.alerts[k]; ok && ex.State == store.AlertFiring {
		ex.LastAt = time.Now().UTC()
		ex.Summary = a.Summary
		ex.Severity = a.Severity
		ex.Evidence = a.Evidence
		ex.IncidentID = a.IncidentID
		ex.Container = a.Container
		return *ex, false, nil
	}
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	now := time.Now().UTC()
	a.State = store.AlertFiring
	a.FirstAt, a.LastAt = now, now
	if a.Evidence == nil {
		a.Evidence = json.RawMessage(`{}`)
	}
	cp := a
	m.alerts[k] = &cp
	m.allAlerts = append(m.allAlerts, &cp)
	return cp, true, nil
}

func (m *memStore) ResolveAlert(_ context.Context, orgID uuid.UUID, dedupeKey string) (store.Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(orgID, dedupeKey)
	a, ok := m.alerts[k]
	if !ok || a.State != store.AlertFiring {
		return store.Alert{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	a.State = store.AlertResolved
	a.ResolvedAt = &now
	a.LastAt = now
	delete(m.alerts, k)
	return *a, nil
}

func (m *memStore) ListFiringAlerts(_ context.Context, orgID uuid.UUID) ([]store.Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Alert
	for _, a := range m.alerts {
		if a.OrgID == orgID && a.State == store.AlertFiring {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (m *memStore) GetIncident(_ context.Context, orgID, id uuid.UUID) (store.Incident, []store.Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, ok := m.incidents[id]
	if !ok || inc.OrgID != orgID {
		return store.Incident{}, nil, store.ErrNotFound
	}
	var alerts []store.Alert
	for _, a := range m.allAlerts {
		if a.IncidentID == id {
			alerts = append(alerts, *a)
		}
	}
	return *inc, alerts, nil
}

func (m *memStore) evalKey(orgID uuid.UUID, kind, key string) string {
	return orgID.String() + "|" + kind + "|" + key
}

func (m *memStore) LoadAlertEvalState(_ context.Context, orgID uuid.UUID) ([]store.AlertEvalState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AlertEvalState
	for _, st := range m.evalState {
		if st.OrgID == orgID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (m *memStore) UpsertAlertEvalState(_ context.Context, st store.AlertEvalState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evalState[m.evalKey(st.OrgID, st.Kind, st.Key)] = st
	return nil
}

func (m *memStore) DeleteAlertEvalState(_ context.Context, orgID uuid.UUID, kind, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.evalState, m.evalKey(orgID, kind, key))
	return nil
}

func (m *memStore) openCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, inc := range m.incidents {
		if inc.State == store.IncidentOpen {
			n++
		}
	}
	return n
}

func (m *memStore) firingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.alerts)
}

type recordingNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

type notifyCall struct {
	orgID  uuid.UUID
	inc    store.Incident
	alerts []store.Alert
}

func (n *recordingNotifier) IncidentChanged(_ context.Context, orgID uuid.UUID, inc store.Incident, alerts []store.Alert) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{orgID: orgID, inc: inc, alerts: append([]store.Alert{}, alerts...)})
	return nil
}

func (n *recordingNotifier) len() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}
