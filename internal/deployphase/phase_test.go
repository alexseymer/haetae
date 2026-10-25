package deployphase

import "testing"

func TestAllowed(t *testing.T) {
	ok := [][2]string{
		{Queued, Fetching},
		{Queued, Failed},
		{Fetching, Building},
		{Fetching, Failed},
		{Building, Starting},
		{Building, Failed},
		{Starting, Checking},
		{Starting, Failed},
		{Starting, RollingBack},
		{Checking, Healthy},
		{Checking, Failed},
		{Checking, RollingBack},
		{Failed, RollingBack},
		{RollingBack, RolledBack},
		{RollingBack, RollbackFailed},
	}
	for _, p := range ok {
		if !Allowed(p[0], p[1]) {
			t.Errorf("Allowed(%s→%s) = false, want true", p[0], p[1])
		}
	}
	bad := [][2]string{
		{Queued, Queued},
		{Queued, Building}, // no skip-forward
		{Fetching, Queued},
		{Fetching, Starting},
		{Building, Healthy},
		{Healthy, Failed},
		{Failed, Healthy},
		{Failed, Fetching},
		{RolledBack, RollingBack},
		{Healthy, RollingBack},
		{RollbackFailed, RollingBack},
		{RollingBack, Fetching},
		{RollingBack, Building},
		{"", Fetching},
		{Fetching, ""},
	}
	for _, p := range bad {
		if Allowed(p[0], p[1]) {
			t.Errorf("Allowed(%s→%s) = true, want false", p[0], p[1])
		}
	}
}
