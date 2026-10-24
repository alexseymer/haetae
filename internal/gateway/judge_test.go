package gateway

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

func TestIsRoutine(t *testing.T) {
	for _, tc := range []struct {
		level    int16
		template string
		want     bool
	}{
		{store.LevelInfo, "GET /api/items/<NUM> <NUM> <DUR>", true},
		{store.LevelDebug, "cache hit key=<STR>", true},
		{store.LevelTrace, "tick <NUM>", true},
		{store.LevelInfo, "connection to db failed, retrying in <DUR>", false},
		{store.LevelInfo, "request timed out after <DUR>", false},
		{store.LevelInfo, "login denied for user <STR>", false},
		{store.LevelInfo, "Slow query took <DUR>", false},
		{store.LevelInfo, "worker OOM killed", false},
		{store.LevelWarn, "GET /health <NUM>", false},
		{store.LevelError, "GET /health <NUM>", false},
		{store.LevelFatal, "started", false},
		{0, "GET /health <NUM>", false}, // unknown level: never routine
	} {
		if got := IsRoutine(tc.level, tc.template); got != tc.want {
			t.Errorf("IsRoutine(%d, %q) = %v, want %v", tc.level, tc.template, got, tc.want)
		}
	}
}

func TestJudgeThrottle(t *testing.T) {
	j := &judgeThrottle{every: 10 * time.Second}
	org, now := uuid.New(), time.Now()
	if !j.allow(org, now) || j.allow(org, now.Add(5*time.Second)) || !j.allow(org, now.Add(11*time.Second)) {
		t.Fatal("throttle did not allow/deny as expected")
	}
	if !j.allow(uuid.New(), now) {
		t.Fatal("another org must not be throttled")
	}
}
