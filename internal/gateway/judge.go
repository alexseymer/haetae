package gateway

import (
	"context"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
)

// Thresholds before a template is judged: it must have been seen often and
// across several merge windows, so a burst of one-off lines is not mistaken
// for routine traffic.
const (
	judgeMinOccurrences = 20
	judgeMinWindows     = 3
	judgeBatch          = 200
	maxGreenIDs         = 10000
)

// suspicious matches words that make an info or debug line worth a human's
// attention even when it is frequent. Such templates become "watch": the
// agent keeps sending them. RE2 only.
var suspicious = regexp.MustCompile(`(?i)\b(fail(ed|ure|ures|ing)?|errors?|denied|refused|refusing|time(d)? ?out|retry|retrying|retries|unauthori[sz]ed|forbidden|exceptions?|panic(ked)?|fatal|crash(ed|es)?|killed|sigkill|sigterm|segfault|segmentation fault|deadlock(ed)?|oom|out of memory|corrupt(ed|ion)?|invalid|reject(ed)?|cannot|can't|unable|deprecated|slow|stale|overflow|exhaust(ed)?|unreachable|unavailable|reset|lost|abort(ed)?|warn(ing)?|degraded|unhealthy|disconnect(ed)?|exited|terminated|restart(ed|ing)?|rollback|broken pipe|throttl(ed|ing)|overload(ed)?|quota|limit exceeded|not found|missing|no such|too many|bad gateway|service unavailable|gateway time ?out|internal server)\b`)

// IsRoutine reports whether a template of this level may become green.
// Warn, error, fatal and unknown levels never can. This is the rule-based
// judge; the AI operator refines it in M8.
func IsRoutine(level int16, template string) bool {
	if level < store.LevelTrace || level > store.LevelInfo {
		return false
	}
	return !suspicious.MatchString(template)
}

// JudgeStore is what the judge needs from the store.
type JudgeStore interface {
	JudgeableTemplates(ctx context.Context, orgID, hostID uuid.UUID, minOccurrences int64, minWindows, limit int) ([]store.LogTemplate, error)
	SetVerdicts(ctx context.Context, orgID, hostID uuid.UUID, green, watch []string) error
	GreenTemplateIDs(ctx context.Context, orgID, hostID uuid.UUID, limit int) ([]string, error)
}

// judgeHost judges the ready templates of one host. Judgements are per host:
// evidence from one host never makes another host's lines skippable. It
// reports whether any template became green, in which case that host's agent
// needs a new green set.
func judgeHost(ctx context.Context, st JudgeStore, orgID, hostID uuid.UUID) (changed bool, err error) {
	ts, err := st.JudgeableTemplates(ctx, orgID, hostID, judgeMinOccurrences, judgeMinWindows, judgeBatch)
	if err != nil || len(ts) == 0 {
		return false, err
	}
	var green, watch []string
	for _, t := range ts {
		if IsRoutine(t.Level, t.Template) {
			green = append(green, t.TemplateID)
		} else {
			watch = append(watch, t.TemplateID)
		}
	}
	if err := st.SetVerdicts(ctx, orgID, hostID, green, watch); err != nil {
		return false, err
	}
	return len(green) > 0, nil
}

// judgeThrottle limits how often a host is judged.
type judgeThrottle struct {
	every time.Duration
	last  map[uuid.UUID]time.Time
}

func (j *judgeThrottle) allow(org uuid.UUID, now time.Time) bool {
	if j.last == nil {
		j.last = make(map[uuid.UUID]time.Time)
	}
	if now.Sub(j.last[org]) < j.every {
		return false
	}
	j.last[org] = now
	return true
}
