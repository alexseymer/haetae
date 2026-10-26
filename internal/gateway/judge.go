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
	var cand []store.LogTemplate
	var watch []string
	for _, t := range ts {
		if IsRoutine(t.Level, t.Template) {
			cand = append(cand, t)
		} else {
			watch = append(watch, t.TemplateID)
		}
	}
	var green []string
	if box := aiJudge.Load(); box != nil {
		green, watch = refineWithAI(ctx, box.j, orgID, cand, watch)
	} else {
		for _, t := range cand {
			green = append(green, t.TemplateID)
		}
	}
	if err := st.SetVerdicts(ctx, orgID, hostID, green, watch); err != nil {
		return false, err
	}
	return len(green) > 0, nil
}

// refineWithAI lets the AI demote rule-routine templates to watch. The AI
// only ever receives the candidates (info and below, no suspicious word,
// already redacted). Any failure, an unavailable AI or a verdict for an
// unknown template leaves the rule result: green stays green.
func refineWithAI(ctx context.Context, ai AIJudge, orgID uuid.UUID, cand []store.LogTemplate, watch []string) (green, outWatch []string) {
	outWatch = watch
	for _, t := range cand {
		green = append(green, t.TemplateID)
	}
	if len(cand) == 0 {
		return green, outWatch
	}
	cctx, cancel := context.WithTimeout(ctx, aiJudgeTimeout)
	defer cancel()
	type result struct {
		vs  []operator.Verdict
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: context.Canceled}
			}
		}()
		if ok, _ := ai.Available(cctx, orgID); !ok {
			ch <- result{err: context.Canceled}
			return
		}
		qs := make([]operator.TemplateQuery, len(cand))
		for i, t := range cand {
			qs[i] = operator.TemplateQuery{TemplateID: t.TemplateID, Level: t.Level, Template: t.Template, Count: t.Occurrences}
		}
		vs, err := ai.JudgeTemplates(cctx, orgID, qs)
		ch <- result{vs, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-cctx.Done():
		return green, outWatch
	}
	if res.err != nil {
		return green, outWatch
	}
	demote := make(map[string]bool)
	for _, v := range res.vs {
		if !v.Green {
			demote[v.TemplateID] = true
		}
	}
	green = green[:0]
	for _, t := range cand {
		if demote[t.TemplateID] {
			outWatch = append(outWatch, t.TemplateID)
		} else {
			green = append(green, t.TemplateID)
		}
	}
	return green, outWatch
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
