package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A match transaction stamps first_seen when it starts and may commit after
// an evaluation has read the findings; the next evaluation must still see it.
func TestCVEWatermarkTrailsEvaluation(t *testing.T) {
	e := &Evaluator{cveMark: map[uuid.UUID]time.Time{}}
	org := uuid.New()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	e.commitCVEWatermark(ctx, org, t0)
	lateFirstSeen := t0.Add(-5 * time.Second) // stamped before t0, committed after
	since := e.cveWatermark(ctx, org, t0.Add(15*time.Second))
	if lateFirstSeen.Before(since) {
		t.Fatalf("watermark %s would hide a finding first seen at %s", since, lateFirstSeen)
	}
	// It still moves forward.
	e.commitCVEWatermark(ctx, org, t0.Add(time.Hour))
	if got := e.cveWatermark(ctx, org, t0.Add(time.Hour)); !got.After(since) {
		t.Fatalf("watermark did not advance: %s", got)
	}
}
