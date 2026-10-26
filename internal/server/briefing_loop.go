package server

import (
	"context"
	"log/slog"

	"github.com/alexseymer/haetae/internal/briefing"
	"github.com/alexseymer/haetae/internal/store"
)

// RunBriefing runs the daily briefing scheduler until ctx ends. enq may be
// nil (no-op) until the chat package wires a real Enqueuer.
func RunBriefing(ctx context.Context, st *store.Store, enq briefing.Enqueuer, rw briefing.Rewriter, log *slog.Logger) {
	if st == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	if enq == nil {
		log.Info("briefing scheduler idle: no enqueuer")
		return
	}
	s := &briefing.Scheduler{Store: st, Enq: enq, Rewriter: rw, Log: log}
	s.Run(ctx)
}
