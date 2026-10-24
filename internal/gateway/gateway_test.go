package gateway

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func TestHostInfoClipsOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("a", maxInfoLen-1) + "ü" + "tail" // "ü" straddles the limit
	got := hostInfo(&agentv1.HostInfo{Hostname: long, Os: "Debian"})
	if !utf8.ValidString(got.Hostname) {
		t.Fatalf("clipped hostname is not valid UTF-8: %q", got.Hostname[len(got.Hostname)-4:])
	}
	if len(got.Hostname) > maxInfoLen {
		t.Fatalf("clipped hostname has %d bytes, want <= %d", len(got.Hostname), maxInfoLen)
	}
	if got.OS != "Debian" {
		t.Fatalf("short field changed: %q", got.OS)
	}
}

func TestHeartbeatBounds(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		0:                      10 * time.Second,
		500 * time.Millisecond: time.Second,
		2 * time.Hour:          time.Hour,
		5 * time.Second:        5 * time.Second,
	} {
		if got := (Config{Heartbeat: in}).withDefaults().Heartbeat; got != want {
			t.Errorf("Heartbeat %s -> %s, want %s", in, got, want)
		}
	}
}

func TestSampleTimeWindow(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		ts   time.Time
		want bool
	}{
		{now, true},
		{now.Add(-30 * time.Minute), true},
		{now.Add(-2 * time.Hour), false},
		{now.Add(time.Minute), true},
		{now.Add(10 * time.Minute), false},
		{time.UnixMilli(0), false},
	} {
		if got := sampleTimeOK(tc.ts, now); got != tc.want {
			t.Errorf("sampleTimeOK(%s) = %v, want %v", now.Sub(tc.ts), got, tc.want)
		}
	}
}
