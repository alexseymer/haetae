package conntrack

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
)

func TestDegradedIsSticky(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewListener(nil, nil, nil)
	l.Now = func() time.Time { return now }
	var changes atomic.Int32
	l.OnStateChange = func() { changes.Add(1) }

	l.setState("ok", "")
	l.setState("degraded", "netlink overflow")
	if changes.Load() < 2 { // unknown -> ok -> degraded
		t.Fatalf("changes=%d", changes.Load())
	}
	// Source restarted fine 50ms later: still reported degraded.
	now = now.Add(50 * time.Millisecond)
	l.setState("ok", "")
	if st := l.Status(); st.GetState() != "degraded" || st.GetReason() != "netlink overflow" {
		t.Fatalf("status=%v", st)
	}
	now = now.Add(4 * time.Minute)
	if l.Status().GetState() != "degraded" {
		t.Fatal("must stay degraded within 5m")
	}
	n := changes.Load()
	now = now.Add(2 * time.Minute)
	if l.Status().GetState() != "ok" {
		t.Fatal("must recover after the hold")
	}
	// Loss again re-arms the hold and emits a state change.
	l.setState("degraded", "again")
	if changes.Load() == n {
		t.Fatal("no state change emitted on new degradation")
	}
}

func TestRefreshEventFilter(t *testing.T) {
	ev := func(typ, act string) dockerapi.Event { return dockerapi.Event{Type: typ, Action: act} }
	for _, c := range []struct {
		e    dockerapi.Event
		want bool
	}{
		{ev("container", "start"), true},
		{ev("container", "die"), true},
		{ev("container", "destroy"), true},
		{ev("container", "exec_start: sh"), false},
		{ev("container", "exec_die"), false},
		{ev("container", "health_status: healthy"), false},
		{ev("network", "connect"), true},
		{ev("network", "disconnect"), true},
		{ev("network", "create"), false},
	} {
		if got := refreshEvent(c.e); got != c.want {
			t.Errorf("%s/%s = %v want %v", c.e.Type, c.e.Action, got, c.want)
		}
	}
}
