package server

import (
	"context"
	"log/slog"

	"github.com/alexseymer/haetae/internal/dailydigest"
	"github.com/alexseymer/haetae/internal/store"
)

// RunDailyDigest runs the daily digest scheduler until ctx ends. It is a
// goroutine of its own: a slow AI call never delays the briefing or alerts.
func RunDailyDigest(ctx context.Context, st *store.Store, enq dailydigest.Enqueuer, dg dailydigest.Digester, log *slog.Logger) {
	if st == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	if enq == nil || dg == nil {
		log.Info("daily digest scheduler idle: no enqueuer or digester")
		return
	}
	s := &dailydigest.Scheduler{Store: st, Enq: enq, Digests: dg, Log: log}
	s.Run(ctx)
}
