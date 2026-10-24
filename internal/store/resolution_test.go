package store

import (
	"testing"
	"time"
)

func TestResolutionForRespectsSpanAndRetention(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		from, to time.Time
		want     Resolution
	}{
		{"last hour", now.Add(-time.Hour), now, ResolutionRaw},
		{"last 6 hours", now.Add(-6 * time.Hour), now, ResolutionRaw},
		{"last day", now.Add(-24 * time.Hour), now, Resolution1m},
		{"last 8 days", now.Add(-8 * 24 * time.Hour), now, Resolution1h},
		{"2 hours, 3 days ago: raw is gone", now.Add(-72 * time.Hour), now.Add(-70 * time.Hour), Resolution1m},
		{"2 hours, 40 days ago: 1m is gone", now.Add(-40 * 24 * time.Hour), now.Add(-40*24*time.Hour + 2*time.Hour), Resolution1h},
		{"reversed range", now, now.Add(-time.Hour), ResolutionRaw},
	} {
		if got := ResolutionFor(now, tc.from, tc.to); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
