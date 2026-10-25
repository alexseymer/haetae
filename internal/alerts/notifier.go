package alerts

import (
	"context"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// Notifier is called after an incident is opened, escalated, joined or resolved.
// Chat implements this; alerts never imports chat.
type Notifier interface {
	IncidentChanged(ctx context.Context, orgID uuid.UUID, inc store.Incident, alerts []store.Alert) error
}

// NopNotifier ignores all incident changes.
type NopNotifier struct{}

// IncidentChanged implements Notifier.
func (NopNotifier) IncidentChanged(context.Context, uuid.UUID, store.Incident, []store.Alert) error {
	return nil
}
