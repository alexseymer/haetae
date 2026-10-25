package alerts

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// Reader is the store surface the evaluator needs. *store.Store implements it.
type Reader interface {
	ListOrgIDs(ctx context.Context) ([]uuid.UUID, error)
	GetOrgSettings(ctx context.Context, orgID uuid.UUID) (store.OrgSettings, error)
	ListHosts(ctx context.Context, orgID uuid.UUID) ([]store.Host, error)
	ContainerRestarts(ctx context.Context, orgID uuid.UUID) ([]store.ContainerRestartFact, error)
	ContainersOOMSince(ctx context.Context, orgID uuid.UUID, since time.Time) ([]store.ContainerOOMFact, error)
	ContainerHealth(ctx context.Context, orgID uuid.UUID) ([]store.ContainerHealthFact, error)
	HostMetricSustained(ctx context.Context, orgID, hostID uuid.UUID, metric string, threshold float64, sustained time.Duration, now time.Time) (bool, error)
	HostDiskPct(ctx context.Context, orgID, hostID uuid.UUID) (float64, error)
	HostsOfflineSince(ctx context.Context, orgID uuid.UUID, cutoff time.Time) ([]store.Host, error)
	RecentFailedDeploys(ctx context.Context, orgID uuid.UUID, since time.Time) ([]store.Deploy, error)
	ActiveMutes(ctx context.Context, orgID uuid.UUID, now time.Time) ([]store.Mute, error)

	OpenIncidentForHost(ctx context.Context, orgID, hostID uuid.UUID, title, severity string) (store.Incident, bool, error)
	UpdateIncident(ctx context.Context, orgID, incidentID uuid.UUID, title, severity string) (store.Incident, error)
	ResolveIncident(ctx context.Context, orgID, incidentID uuid.UUID) (store.Incident, error)
	UpsertFiringAlert(ctx context.Context, a store.Alert) (store.Alert, bool, error)
	ResolveAlert(ctx context.Context, orgID uuid.UUID, dedupeKey string) (store.Alert, error)
	ListFiringAlerts(ctx context.Context, orgID uuid.UUID) ([]store.Alert, error)
	GetIncident(ctx context.Context, orgID, id uuid.UUID) (store.Incident, []store.Alert, error)

	LoadAlertEvalState(ctx context.Context, orgID uuid.UUID) ([]store.AlertEvalState, error)
	UpsertAlertEvalState(ctx context.Context, st store.AlertEvalState) error
	DeleteAlertEvalState(ctx context.Context, orgID uuid.UUID, kind, key string) error
}
