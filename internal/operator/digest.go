package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

// Digest kinds.
const (
	DigestLogs     = "logs"
	DigestSecurity = "security"
)

const (
	digestCacheTTL     = 5 * time.Minute
	digestCacheMax     = 128
	digestPromptBudget = 14 << 10 // bytes of evidence JSON handed to the model (the prompt stays under 16 KB)
	digestMaxStatement = 8
	// DefaultDigestWindow is the log window when none is given.
	DefaultDigestWindow = 24 * time.Hour
)

// Errors of the digest functions.
var (
	// ErrBadWindow: the window is not one of 1h, 6h, 24h.
	ErrBadWindow = errors.New("operator: window must be 1h, 6h or 24h")
	// ErrScopeNotFound: the host or container is not in the organisation.
	ErrScopeNotFound = errors.New("operator: host or container not found")

	errNoStatements = errors.New("the model returned no statement backed by evidence")
)

// ParseDigestWindow parses "1h", "6h" or "24h"; "" means 24h.
func ParseDigestWindow(s string) (time.Duration, error) {
	switch strings.TrimSpace(s) {
	case "", "24h":
		return 24 * time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "1h":
		return time.Hour, nil
	}
	return 0, ErrBadWindow
}

func validDigestWindow(d time.Duration) (time.Duration, error) {
	switch d {
	case 0:
		return DefaultDigestWindow, nil
	case time.Hour, 6 * time.Hour, 24 * time.Hour:
		return d, nil
	}
	return 0, ErrBadWindow
}

// LogScope selects what a log summary covers. A zero HostID means every host
// of the organisation; an empty ContainerID every container.
type LogScope struct {
	HostID      uuid.UUID
	ContainerID string
	Window      time.Duration // 1h, 6h or 24h; 0 means 24h
}

// SecurityScope selects what a security digest covers. A zero HostID means
// every host of the organisation.
type SecurityScope struct {
	HostID uuid.UUID
}

// Digest is a validated log summary or security digest. Every statement cites
// evidence in Evidence. UsedAI is false for the rule-based digest; Reason then
// says why the AI was not used.
type Digest struct {
	Kind        string    `json:"kind"` // logs | security
	Title       string    `json:"title"`
	WindowFrom  time.Time `json:"windowFrom"`
	WindowTo    time.Time `json:"windowTo"`
	GeneratedAt time.Time `json:"generatedAt"`
	UsedAI      bool      `json:"usedAI"`
	Reason      string    `json:"reason,omitempty"`
	// Quiet: nothing notable was found; there are no AI claims.
	Quiet bool `json:"quiet"`
	// NeedsAttention: new error patterns, a spike, new critical or high
	// findings or a failed scan. The scheduled digest posts only then.
	NeedsAttention bool        `json:"needsAttention"`
	Statements     []Statement `json:"statements"`
	Evidence       []Evidence  `json:"evidence"`
}

// Text renders the digest as plain text: the title, then one line per
// statement. Links are carried in Evidence.
func (d Digest) Text() string {
	var b strings.Builder
	b.WriteString(d.Title)
	for _, s := range d.Statements {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimSpace(s.Text))
	}
	return b.String()
}

// Digester is what the API, the scheduler and the chat shortcuts use.
type Digester interface {
	SummarizeLogs(ctx context.Context, orgID uuid.UUID, scope LogScope) (Digest, error)
	SecurityDigest(ctx context.Context, orgID uuid.UUID, scope SecurityScope) (Digest, error)
}

var _ Digester = (*Service)(nil)

// ruleLine is one deterministic statement and the evidence records (indexes
// into digestInput.outs) it cites. A must line is added by the server when the
// model did not cover its evidence: failed scans and critical findings cannot
// be left out by the model.
type ruleLine struct {
	text string
	ev   []int
	must bool
	// aiOnly lines are used only to cover must evidence in the AI digest;
	// the rule digest already says the same in another line.
	aiOnly bool
}

// digestInput is everything one digest is built from.
type digestInput struct {
	kind, title    string
	from, to       time.Time
	outs           []*toolOut
	lines          []ruleLine
	quiet          bool
	quietText      string
	needsAttention bool
	system, task   string
}

// evidenceRecord builds the in-memory evidence of outs[i] with id ev_<i+1>.
func evidenceRecord(i int, out *toolOut) Evidence {
	stored := map[string]any{}
	if len(out.data) > 0 {
		stored["data"] = out.data
	}
	if len(out.untrusted) > 0 {
		stored["untrusted"] = out.untrusted
	}
	data, _ := json.Marshal(stored)
	return Evidence{
		ID: fmt.Sprintf("ev_%d", i+1), Kind: out.kind, Trusted: out.trusted,
		SubjectType: out.subjectType, SubjectID: out.subjectID,
		Title: out.title, Summary: out.summary, Data: data, Link: out.link,
	}
}

// ruleDigest is the deterministic digest of in.
func (s *Service) ruleDigest(base Digest, in digestInput) Digest {
	d := base
	d.UsedAI = false
	if in.quiet {
		d.Quiet = true
		d.Statements = []Statement{{Text: in.quietText, Evidence: []string{}}}
		d.Evidence = []Evidence{}
		return d
	}
	used := map[int]bool{}
	for _, l := range in.lines {
		if l.aiOnly {
			continue
		}
		ids := make([]string, 0, len(l.ev))
		for _, i := range l.ev {
			used[i] = true
			ids = append(ids, fmt.Sprintf("ev_%d", i+1))
		}
		d.Statements = append(d.Statements, Statement{Text: l.text, Evidence: ids})
	}
	idx := make([]int, 0, len(used))
	for i := range used {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	d.Evidence = make([]Evidence, 0, len(idx))
	for _, i := range idx {
		d.Evidence = append(d.Evidence, evidenceRecord(i, in.outs[i]))
	}
	if d.Statements == nil {
		d.Statements = []Statement{}
	}
	return d
}

// cleanClaimRe matches statements that claim everything is fine. A digest that
// has anything notable never carries one: a model could be talked into it by
// a log line or a package name.
var cleanClaimRe = regexp.MustCompile(`(?i)\b(all clear|all good|all fine|nothing (to worry|notable|wrong|unusual|to report)|no (known )?(vulnerabilit|issue|problem|finding|error)|(is|are|looks?|seems?) (clean|fine|healthy|safe|good|secure)|no action (is )?(needed|required))`)

// runDigest turns a prepared input into a Digest: the rule digest when there
// is nothing notable or the AI cannot be used, else the AI digest.
func (s *Service) runDigest(ctx context.Context, orgID uuid.UUID, in digestInput) (Digest, error) {
	base := Digest{Kind: in.kind, Title: in.title, WindowFrom: in.from.UTC(), WindowTo: in.to.UTC(),
		GeneratedAt: s.cfg.Now().UTC(), NeedsAttention: in.needsAttention}
	rule := s.ruleDigest(base, in)
	if in.quiet {
		rule.Reason = "nothing notable, so the AI was not used"
		return rule, nil
	}
	if err := s.gate(ctx, orgID); err != nil {
		rule.Reason = gateReason(err)
		return rule, nil
	}
	d, err := s.aiDigest(ctx, orgID, in, base)
	if err != nil {
		if ctx.Err() != nil {
			return Digest{}, ctx.Err()
		}
		s.logFailure("digest_"+in.kind, err)
		rule.Reason = digestReason(err)
		return rule, nil
	}
	return d, nil
}

func gateReason(err error) string {
	switch {
	case errors.Is(err, ErrCapped):
		return "daily usage cap reached"
	case errors.Is(err, errNotConfigured):
		return "not configured"
	case errors.Is(err, errTurnedOff):
		return "turned off"
	}
	return "temporarily unavailable"
}

func digestReason(err error) string {
	if errors.Is(err, errNoStatements) {
		return "the AI returned no statement backed by evidence"
	}
	if errors.Is(err, ErrDisabled) || errors.Is(err, ErrCapped) {
		return gateReason(err)
	}
	return Reason(err)
}

// aiDigest asks the model once (plus one repair) over the evidence of in.
func (s *Service) aiDigest(ctx context.Context, orgID uuid.UUID, in digestInput, base Digest) (Digest, error) {
	c := &conv{id: uuid.New(), org: orgID}
	items := make([]json.RawMessage, 0, len(in.outs))
	for _, out := range in.outs {
		raw, ok := s.addEvidence(ctx, c, out)
		if !ok {
			return Digest{}, errors.New("operator: could not store digest evidence")
		}
		items = append(items, json.RawMessage(raw))
	}
	payload, err := json.Marshal(map[string]any{
		"task":        in.task,
		"window_from": in.from.UTC().Format(time.RFC3339),
		"window_to":   in.to.UTC().Format(time.RFC3339),
		"evidence":    items,
	})
	if err != nil {
		return Digest{}, fmt.Errorf("operator: digest request: %w", err)
	}
	user, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleUser, Text: string(payload)})
	if err != nil {
		return Digest{}, err
	}
	resp, err := s.complete(ctx, orgID, provider.Request{System: in.system, Messages: []provider.Message{user}, MaxTokens: answerTokens})
	if err != nil {
		return Digest{}, err
	}
	w, perr := parseAnswer(resp.Text)
	if perr != nil {
		bad, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleAssistant, Text: truncateBytes(resp.Text, 2000)})
		if err != nil {
			return Digest{}, err
		}
		fix, err := s.egressMessage(ctx, provider.Message{Role: provider.RoleUser, Text: repairPrompt})
		if err != nil {
			return Digest{}, err
		}
		resp2, err := s.complete(ctx, orgID, provider.Request{System: in.system, Messages: []provider.Message{user, bad, fix}, MaxTokens: answerTokens})
		if err != nil {
			return Digest{}, err
		}
		if w, perr = parseAnswer(resp2.Text); perr != nil {
			return Digest{}, perr
		}
	}
	// A digest never proposes actions: w.Actions is ignored.
	var kept []Statement
	kept0, _, _ := validate(c, w.Statements)
	for _, st := range kept0 {
		if cleanClaimRe.MatchString(st.Text) {
			continue
		}
		kept = append(kept, st)
	}
	if len(kept) > digestMaxStatement {
		kept = kept[:digestMaxStatement]
	}
	if len(kept) == 0 {
		return Digest{}, errNoStatements
	}
	citedByID := map[string]Evidence{}
	for _, st := range kept {
		for _, id := range st.Evidence {
			if r := c.find(id); r != nil {
				citedByID[id] = r.Evidence
			}
		}
	}
	// Every must line is stated, first, as its own statement whatever the
	// model wrote: a model statement that cites the same evidence must not
	// hide a failed or pending scan, nor a critical finding.
	var extra []Statement
	for _, l := range in.lines {
		if !l.must {
			continue
		}
		ids := make([]string, 0, len(l.ev))
		for _, i := range l.ev {
			id := fmt.Sprintf("ev_%d", i+1)
			ids = append(ids, id)
			if _, ok := citedByID[id]; !ok {
				citedByID[id] = evidenceRecord(i, in.outs[i])
			}
		}
		extra = append(extra, Statement{Text: l.text, Evidence: ids})
	}
	d := base
	d.UsedAI = true
	d.Statements = append(extra, kept...)
	d.Evidence = make([]Evidence, 0, len(citedByID))
	for _, e := range citedByID {
		d.Evidence = append(d.Evidence, e)
	}
	sort.Slice(d.Evidence, func(i, j int) bool { return evSeq(d.Evidence[i].ID) < evSeq(d.Evidence[j].ID) })
	return d, nil
}

// fitOuts keeps the leading records whose JSON fits the prompt budget.
func fitOuts(outs []*toolOut, budget int) []*toolOut {
	total := 0
	for i, o := range outs {
		d, _ := json.Marshal(o.data)
		u, _ := json.Marshal(o.untrusted)
		total += len(d) + len(u) + 64
		if total > budget && i > 0 {
			return outs[:i]
		}
	}
	return outs
}

// digestCache keeps the last digest per (org, scope, kind, window) for
// digestCacheTTL, bounded to digestCacheMax entries.
type digestCache struct {
	mu sync.Mutex
	m  map[string]cachedDigest
}

type cachedDigest struct {
	d  Digest
	at time.Time
}

func (c *digestCache) get(key string, now time.Time) (Digest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) >= digestCacheTTL || now.Before(e.at) {
		return Digest{}, false
	}
	return e.d, true
}

func (c *digestCache) put(key string, d Digest, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedDigest{}
	}
	if len(c.m) >= digestCacheMax {
		for k, e := range c.m {
			if now.Sub(e.at) >= digestCacheTTL {
				delete(c.m, k)
			}
		}
		for len(c.m) >= digestCacheMax {
			oldest, ot := "", time.Time{}
			for k, e := range c.m {
				if oldest == "" || e.at.Before(ot) {
					oldest, ot = k, e.at
				}
			}
			delete(c.m, oldest)
		}
	}
	c.m[key] = cachedDigest{d: d, at: now}
}

// safeID returns s when it looks like a container or host id, else "".
func safeID(s string) string {
	if containerIDRe.MatchString(s) {
		return s
	}
	return ""
}

// quoteText makes third-party text safe for a statement: control characters
// and evidence markers removed, cut to max runes.
func quoteText(s string, max int) string { return cleanText(s, max) }

func windowName(d time.Duration) string {
	switch d {
	case time.Hour:
		return "1 h"
	case 6 * time.Hour:
		return "6 h"
	}
	return "24 h"
}

func ratioText(cur, prev int64) string {
	if prev <= 0 {
		return ""
	}
	r := float64(cur) / float64(prev)
	if r >= 10 {
		return fmt.Sprintf("%.0fx", r)
	}
	t := fmt.Sprintf("%.1f", r)
	return strings.TrimSuffix(t, ".0") + "x"
}
