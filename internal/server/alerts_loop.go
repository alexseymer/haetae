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
// Returns the evaluator so other loops can Nudge it (e.g. after vuln rematch).
func RunAlertsEvaluator(ctx context.Context, st *store.Store, notifier alerts.Notifier, log *slog.Logger) *alerts.Evaluator {
	if log == nil {
		log = slog.Default()
	}
	e := alerts.NewEvaluator(st, notifier, alerts.Config{Interval: 15 * time.Second}, log)
	go e.Run(ctx)
	return e
}
