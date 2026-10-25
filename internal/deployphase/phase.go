// Package deployphase defines the deploy status transition table shared by the
// agent (so it never emits an illegal sequence) and the store (phaseAllowed).
package deployphase

// Status strings stored in the deploys.status column and used in events.
const (
	Queued         = "queued"
	Fetching       = "fetching"
	Building       = "building"
	Starting       = "starting"
	Checking       = "checking"
	Healthy        = "healthy"
	Failed         = "failed"
	RollingBack    = "rolling_back"
	RolledBack     = "rolled_back"
	RollbackFailed = "rollback_failed"
)

// Allowed reports whether moving from cur to next is a valid forward step.
// Stale or repeated events (same phase, empty next, or illegal jump) are not
// allowed; callers ignore them without error.
//
// Allowed:
//
//	queued → fetching | failed
//	fetching → building | failed
//	building → starting | failed
//	starting → checking | failed | rolling_back
//	checking → healthy | failed | rolling_back
//	failed → rolling_back
//	rolling_back → rolled_back | rollback_failed
//
// healthy, rolled_back, rollback_failed are final; failed is final except
// failed → rolling_back.
func Allowed(cur, next string) bool {
	if next == "" || cur == next {
		return false
	}
	switch cur {
	case Queued:
		return next == Fetching || next == Failed
	case Fetching:
		return next == Building || next == Failed
	case Building:
		return next == Starting || next == Failed
	case Starting:
		return next == Checking || next == Failed || next == RollingBack
	case Checking:
		return next == Healthy || next == Failed || next == RollingBack
	case Failed:
		return next == RollingBack
	case RollingBack:
		return next == RolledBack || next == RollbackFailed
	default:
		return false
	}
}

// Rank orders phases for obsolescence (higher = later). Unknown phases rank 0.
func Rank(phase string) int {
	switch phase {
	case Queued:
		return 1
	case Fetching:
		return 2
	case Building:
		return 3
	case Starting:
		return 4
	case Checking:
		return 5
	case Healthy, Failed:
		return 6
	case RollingBack:
		return 7
	case RolledBack, RollbackFailed:
		return 8
	default:
		return 0
	}
}

// IsFinal reports whether status is a terminal deploy status
// (failed may still move to rolling_back).
func IsFinal(s string) bool {
	switch s {
	case Healthy, Failed, RolledBack, RollbackFailed:
		return true
	default:
		return false
	}
}
