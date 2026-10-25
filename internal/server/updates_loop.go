package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/updates"
)

// RunUpdatesLoop evaluates image update decisions and sends PullImage /
// ScanImage when org auto_pull is on.
func RunUpdatesLoop(ctx context.Context, st *store.Store, sender updates.Sender, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	l := updates.NewLoop(st, sender, log)
	l.Every = time.Minute
	l.Run(ctx)
}
