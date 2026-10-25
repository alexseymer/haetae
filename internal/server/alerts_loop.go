package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
)

// RunAlertsEvaluator evaluates rule alerts every 15 s until ctx ends.
// notifier may be nil (no chat fan-out until chat is wired).
func RunAlertsEvaluator(ctx context.Context, st *store.Store, notifier alerts.Notifier, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	e := alerts.NewEvaluator(st, notifier, alerts.Config{Interval: 15 * time.Second}, log)
	e.Run(ctx)
}
