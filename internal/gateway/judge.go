package gateway

import (
	"context"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

// aiJudgeTimeout bounds one AI judgement; the rule verdicts are used when it
// elapses.
const aiJudgeTimeout = 10 * time.Second

// AIJudge is the optional AI second look at templates the rule judge made
// green. It is the part of operator.Operator the judge uses. It can only turn
// a green candidate into watch; it never makes anything green.
type AIJudge interface {
	Available(ctx context.Context, orgID uuid.UUID) (ok bool, reason string)
	JudgeTemplates(ctx context.Context, orgID uuid.UUID, ts []operator.TemplateQuery) ([]operator.Verdict, error)
}

type aiJudgeBox struct{ j AIJudge }

var aiJudge atomic.Pointer[aiJudgeBox]

// SetAIJudge installs (or, with nil, removes) the AI judge used by every
// gateway in this process. Safe to call at any time.
func SetAIJudge(j AIJudge) {
	if j == nil {
		aiJudge.Store(nil)
		return
	}
	aiJudge.Store(&aiJudgeBox{j})
}

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
	DemoteGreen(ctx context.Context, orgID, hostID uuid.UUID, ids []string) error
	GreenTemplateIDs(ctx context.Context, orgID, hostID uuid.UUID, limit int) ([]string, error)
}

// judgeHost applies the rule judge to the ready templates of one host. It
// never calls the AI. Judgements are per host: evidence from one host never
// makes another host's lines skippable. It returns the templates that became
// green (the AI candidates); when there are any, that host's agent needs a new
// green set.
func judgeHost(ctx context.Context, st JudgeStore, orgID, hostID uuid.UUID) (green []store.LogTemplate, err error) {
	ts, err := st.JudgeableTemplates(ctx, orgID, hostID, judgeMinOccurrences, judgeMinWindows, judgeBatch)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	var watch, ids []string
	for _, t := range ts {
		if IsRoutine(t.Level, t.Template) {
			green = append(green, t)
			ids = append(ids, t.TemplateID)
		} else {
			watch = append(watch, t.TemplateID)
		}
	}
	if err := st.SetVerdicts(ctx, orgID, hostID, ids, watch); err != nil {
		return nil, err
	}
	return green, nil
}

// aiDemotions asks the AI which rule-green templates look anomalous. It only
// ever receives the candidates (info and below, no suspicious word, already
// redacted) and returns ids among them; a failure, an unavailable AI or a
// verdict for an unknown template yields nothing.
func aiDemotions(ctx context.Context, ai AIJudge, orgID uuid.UUID, cand []store.LogTemplate) []string {
	if len(cand) == 0 {
		return nil
	}
	if ok, _ := ai.Available(ctx, orgID); !ok {
		return nil
	}
	qs := make([]operator.TemplateQuery, len(cand))
	known := make(map[string]bool, len(cand))
	for i, t := range cand {
		qs[i] = operator.TemplateQuery{TemplateID: t.TemplateID, Level: t.Level, Template: t.Template, Count: t.Occurrences}
		known[t.TemplateID] = true
	}
	vs, err := ai.JudgeTemplates(ctx, orgID, qs)
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, v := range vs {
		if !v.Green && known[v.TemplateID] && !seen[v.TemplateID] {
			seen[v.TemplateID] = true
			out = append(out, v.TemplateID)
		}
	}
	return out
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
