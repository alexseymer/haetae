package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

// RawMetricsRetention is how long 10 s samples are kept before only the
// rollups remain.
const RawMetricsRetention = store.RawRetention

// Retention of stored log events (merged and redacted) and of the template
// judgements agents rely on.
const (
	LogEventRetention    = 30 * 24 * time.Hour
	LogTemplateRetention = 90 * 24 * time.Hour
)

// MetricsMaintainer is the store work done on a schedule (metrics and logs).
type MetricsMaintainer interface {
	EnsurePartitions(ctx context.Context, now time.Time) error
	DropExpiredPartitions(ctx context.Context, now time.Time, rawRetention time.Duration) (int, error)
	Rollup(ctx context.Context, now time.Time) error
	PruneRollups(ctx context.Context, now time.Time) error
	PruneLogs(ctx context.Context, now time.Time, eventRetention, templateRetention time.Duration) error
}

// MaintainMetrics runs partition management, rollups and retention once, then
// every interval until ctx ends. Errors are logged and retried next round.
func MaintainMetrics(ctx context.Context, m MetricsMaintainer, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		maintainOnce(ctx, m, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func maintainOnce(ctx context.Context, m MetricsMaintainer, log *slog.Logger) {
	now := time.Now()
	if err := m.EnsurePartitions(ctx, now); err != nil {
		log.Error("ensure metric partitions", "err", err)
	}
	if err := m.Rollup(ctx, now); err != nil {
		log.Error("roll up metrics", "err", err)
	}
	if n, err := m.DropExpiredPartitions(ctx, now, RawMetricsRetention); err != nil {
		log.Error("drop expired metric partitions", "err", err)
	} else if n > 0 {
		log.Info("dropped expired metric partitions", "count", n)
	}
	if err := m.PruneRollups(ctx, now); err != nil {
		log.Error("prune metric rollups", "err", err)
	}
	if err := m.PruneLogs(ctx, now, LogEventRetention, LogTemplateRetention); err != nil {
		log.Error("prune logs", "err", err)
	}
}
