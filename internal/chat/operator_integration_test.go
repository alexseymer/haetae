//go:build integration

package chat_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

type stubChannel struct{ name string }

func (c stubChannel) Name() string { return c.name }
func (c stubChannel) Send(context.Context, store.ChatMessage, *store.ChatAccount) error {
	return nil
}
func (c stubChannel) Run(ctx context.Context) error { <-ctx.Done(); return nil }

type fakeOp struct {
	mu          sync.Mutex
	avail       bool
	reason      string
	answer      operator.Answer
	askErr      error
	block       chan struct{} // when non-nil, Ask and ExplainIncident wait on it
	explained   atomic.Int32
	executed    []uuid.UUID
	executeErr  error
	executeWait time.Duration
}

func (f *fakeOp) Available(context.Context, uuid.UUID) (bool, string) { return f.avail, f.reason }
func (f *fakeOp) Ask(ctx context.Context, _ uuid.UUID, _ string) (operator.Answer, error) {
	if f.block != nil {
		<-f.block
	}
	return f.answer, f.askErr
}
func (f *fakeOp) ExplainIncident(ctx context.Context, _, _ uuid.UUID) (operator.Answer, error) {
	f.explained.Add(1)
	if f.block != nil {
		<-f.block
	}
	return f.answer, f.askErr
}
func (f *fakeOp) ExecuteApproved(_ context.Context, _, id uuid.UUID) error {
	time.Sleep(f.executeWait)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executed = append(f.executed, id)
	return f.executeErr
}
func (f *fakeOp) executedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.executed)
}

var testSecret = []byte("0123456789abcdef0123456789abcdef")

type opEnv struct {
	s    *store.Store
	svc  *chat.Service
	org  store.Org
	host store.Host
	user store.User
	op   *fakeOp
}

func newOpEnv(t *testing.T, op *fakeOp) *opEnv {
	t.Helper()
	s := storetest.New(t)
	org, host, admin := seedOrg(t, s, "op-org", "admin@op.test")
	svc := chat.New(chat.Config{
		Store: s, Secret: testSecret, PublicURL: "https://haetae.test",
		Channels: []chat.Channel{web.Channel{}, stubChannel{store.ChannelTelegram}},
	})
	if op != nil {
		svc.SetOperator(op)
	}
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "111")
	return &opEnv{s: s, svc: svc, org: org, host: host, user: admin, op: op}
}

type outRow struct {
	ID       uuid.UUID
	Channel  string
	Kind     string
	Body     string
	Actions  string
	Evidence string
}

func (e *opEnv) outbound(t *testing.T) []outRow {
	t.Helper()
	rows, err := e.s.Pool().Query(context.Background(), `
		SELECT id, channel, kind, body, actions::text, evidence::text FROM chat_messages
		WHERE org_id = $1 AND direction = 'out' ORDER BY created_at, id`, e.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outRow
	for rows.Next() {
		var r outRow
		if err := rows.Scan(&r.ID, &r.Channel, &r.Kind, &r.Body, &r.Actions, &r.Evidence); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *opEnv) waitFor(t *testing.T, what string, pred func([]outRow) bool) []outRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows := e.outbound(t)
		if pred(rows) {
			return rows
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; rows=%+v", what, e.outbound(t))
	return nil
}

func hasBody(rows []outRow, channel, sub string) bool {
	for _, r := range rows {
		if r.Channel == channel && strings.Contains(r.Body, sub) {
			return true
		}
	}
	return false
}

func sampleAnswer(hostID uuid.UUID) operator.Answer {
	return operator.Answer{
		Statements: []operator.Statement{{Text: "api is crash-looping", Evidence: []string{"ev_1"}}},
		Evidence: []operator.Evidence{{
			ID: "ev_1", Kind: operator.EvidenceContainers, Trusted: true, Title: "Container api",
			Link: "/hosts/" + hostID.String() + "?tab=containers",
		}},
	}
}

func TestOperatorAskNilOperatorKeepsDisabledReply(t *testing.T) {
	e := newOpEnv(t, nil)
	reply, err := e.svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "why is api down?")
	if err != nil || reply != chat.OperatorDisabledReply {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
}

func TestOperatorAskUnavailableReply(t *testing.T) {
	e := newOpEnv(t, &fakeOp{avail: false, reason: "daily usage cap reached"})
	reply, _ := e.svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "hi")
	want := "The operator is unavailable (daily usage cap reached); rule alerts are active."
	if reply != want {
		t.Fatalf("reply=%q", reply)
	}
}

func TestOperatorAskAnswerWithEvidenceLinks(t *testing.T) {
	e := newOpEnv(t, nil)
	op := &fakeOp{avail: true, answer: sampleAnswer(e.host.ID)}
	e.svc.SetOperator(op)
	reply, err := e.svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "why is api down?")
	if err != nil || reply != "" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	rows := e.waitFor(t, "answer", func(r []outRow) bool { return hasBody(r, store.ChannelTelegram, "crash-looping") })
	for _, r := range rows {
		if r.Channel == store.ChannelTelegram && r.Kind == store.KindAnswer {
			if !strings.Contains(r.Body, "https://haetae.test/hosts/"+e.host.ID.String()) {
				t.Fatalf("no full link in %q", r.Body)
			}
			if !strings.Contains(r.Evidence, "/hosts/") {
				t.Fatalf("evidence not stored: %s", r.Evidence)
			}
		}
	}
}

func TestOperatorAskCappedAndHungDoNotBlock(t *testing.T) {
	e := newOpEnv(t, nil)
	op := &fakeOp{avail: true, askErr: operator.ErrCapped}
	e.svc.SetOperator(op)
	if _, err := e.svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "q"); err != nil {
		t.Fatal(err)
	}
	e.waitFor(t, "capped reply", func(r []outRow) bool {
		return hasBody(r, store.ChannelTelegram, "unavailable (daily usage cap reached)")
	})

	// A hung operator: the handler still returns at once.
	e.svc.SetOperator(&fakeOp{avail: true, block: make(chan struct{})})
	start := time.Now()
	reply, _ := e.svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "q2")
	if reply != "" || time.Since(start) > 2*time.Second {
		t.Fatalf("handler blocked: %q after %v", reply, time.Since(start))
	}
}

// approvalEnv creates an awaiting_approval ai_actions row and the proposal
// message through a fake Ask.
func approvalEnv(t *testing.T) (*opEnv, uuid.UUID, outRow) {
	t.Helper()
	e := newOpEnv(t, nil)
	var approvalID uuid.UUID
	err := e.s.Pool().QueryRow(context.Background(), `
		INSERT INTO ai_actions (org_id, host_id, container_id, kind, level, origin, outcome, reason)
		VALUES ($1,$2,'c1','restart_container','suggest','proposed','awaiting_approval','crash loop')
		RETURNING id`, e.org.ID, e.host.ID).Scan(&approvalID)
	if err != nil {
		t.Fatal(err)
	}
	ans := sampleAnswer(e.host.ID)
	ans.Actions = []operator.ActionDecision{{
		Action:     operator.ProposedAction{Kind: "restart_container", ContainerID: "api", Reason: "it is crash-looping", Evidence: []string{"ev_1"}},
		Outcome:    "awaiting_approval",
		ApprovalID: approvalID,
	}}
	op := &fakeOp{avail: true, answer: ans}
	e.op = op
	e.svc.SetOperator(op)
	if _, err := e.svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "fix api"); err != nil {
		t.Fatal(err)
	}
	rows := e.waitFor(t, "proposal", func(r []outRow) bool {
		for _, x := range r {
			if x.Channel == store.ChannelTelegram && strings.Contains(x.Actions, store.ActionApprove) {
				return true
			}
		}
		return false
	})
	for _, r := range rows {
		if r.Channel == store.ChannelTelegram && strings.Contains(r.Actions, store.ActionApprove) {
			if !strings.Contains(r.Body, "Haetae proposes: restart api because it is crash-looping [ev_1]") {
				t.Fatalf("body=%q", r.Body)
			}
			return e, approvalID, r
		}
	}
	t.Fatal("unreachable")
	return nil, uuid.Nil, outRow{}
}

func (e *opEnv) token(msgID uuid.UUID, action string, exp time.Time) string {
	return store.SignActionToken(testSecret, msgID, action, exp)
}

func TestApprovalRejectsUnlinkedAndNonAdmin(t *testing.T) {
	e, _, msg := approvalEnv(t)
	ctx := context.Background()
	tok := e.token(msg.ID, store.ActionApprove, time.Now().Add(time.Hour))

	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "999", tok); reply != "Action not allowed." {
		t.Fatalf("unlinked: %q", reply)
	}
	member, err := e.s.CreateUser(ctx, e.org.ID, "member@op.test", "hash", "member")
	if err != nil {
		t.Fatal(err)
	}
	linkAndAccount(t, e.s, e.svc, e.org.ID, member.ID, store.ChannelTelegram, "222")
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "222", tok); reply != "Action not allowed." {
		t.Fatalf("member: %q", reply)
	}
	if e.op.executedCount() != 0 {
		t.Fatal("executed for a rejected press")
	}
	// The refused presses did not consume the button.
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", tok); !strings.HasPrefix(reply, "Approved") {
		t.Fatalf("admin: %q", reply)
	}
	if e.op.executedCount() != 1 {
		t.Fatalf("executed %d", e.op.executedCount())
	}
}

func TestApprovalReplayExpiredAndDouble(t *testing.T) {
	e, approvalID, msg := approvalEnv(t)
	ctx := context.Background()

	expired := e.token(msg.ID, store.ActionApprove, time.Now().Add(-time.Minute))
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", expired); reply != "Action not allowed." {
		t.Fatalf("expired: %q", reply)
	}
	e.op.executeWait = 50 * time.Millisecond
	tok := e.token(msg.ID, store.ActionApprove, time.Now().Add(time.Hour))
	var wg sync.WaitGroup
	var approved atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", tok)
			if strings.HasPrefix(reply, "Approved") {
				approved.Add(1)
			}
		}()
	}
	wg.Wait()
	if approved.Load() != 1 || e.op.executedCount() != 1 {
		t.Fatalf("approved=%d executed=%d", approved.Load(), e.op.executedCount())
	}
	e.op.mu.Lock()
	got := e.op.executed[0]
	e.op.mu.Unlock()
	if got != approvalID {
		t.Fatalf("executed %v want %v", got, approvalID)
	}
	// Replay, and the sibling decline, are both refused as already handled.
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", tok); reply != "Already handled." {
		t.Fatalf("replay: %q", reply)
	}
	dec := e.token(msg.ID, store.ActionDecline, time.Now().Add(time.Hour))
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", dec); reply != "Already handled." {
		t.Fatalf("decline after approve: %q", reply)
	}
}

func TestApprovalExpiredRefRejected(t *testing.T) {
	e, _, msg := approvalEnv(t)
	ctx := context.Background()
	refs, err := e.s.CreateActionRefs(ctx, e.org.ID, msg.ID, []string{store.ActionApprove}, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", store.ActionRefPrefix+refs[store.ActionApprove]); reply != "Action not allowed." {
		t.Fatalf("expired ref: %q", reply)
	}
	if e.op.executedCount() != 0 {
		t.Fatal("executed on expired ref")
	}
}

func TestApprovalDeclineMarksFailedOnce(t *testing.T) {
	e, approvalID, msg := approvalEnv(t)
	ctx := context.Background()
	dec := e.token(msg.ID, store.ActionDecline, time.Now().Add(time.Hour))
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", dec); reply != "Declined." {
		t.Fatalf("decline: %q", reply)
	}
	var outcome, detail string
	if err := e.s.Pool().QueryRow(ctx, `SELECT outcome, detail FROM ai_actions WHERE id = $1`, approvalID).Scan(&outcome, &detail); err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" || detail != "declined" {
		t.Fatalf("outcome=%s detail=%s", outcome, detail)
	}
	appr := e.token(msg.ID, store.ActionApprove, time.Now().Add(time.Hour))
	if reply, _ := e.svc.HandleAction(ctx, store.ChannelTelegram, "111", appr); reply != "Already handled." {
		t.Fatalf("approve after decline: %q", reply)
	}
	if e.op.executedCount() != 0 {
		t.Fatal("executed after decline")
	}
}

func incidentFor(t *testing.T, e *opEnv) (store.Incident, []store.Alert) {
	t.Helper()
	inc, err := e.s.InsertIncidentForTest(context.Background(), store.Incident{
		ID: uuid.New(), OrgID: e.org.ID, HostID: e.host.ID,
		Title: "api down", Severity: store.SeverityHigh, State: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	return inc, []store.Alert{{OrgID: e.org.ID, HostID: e.host.ID, IncidentID: inc.ID, Rule: "oom_kill",
		Container: "api", Severity: store.SeverityHigh, State: "firing", DedupeKey: "k", Summary: "OOM kill"}}
}

func TestIncidentReportNeverBlocksRuleNotification(t *testing.T) {
	e := newOpEnv(t, &fakeOp{avail: true, block: make(chan struct{})}) // blocks forever
	inc, alerts := incidentFor(t, e)
	start := time.Now()
	if err := e.svc.IncidentChanged(context.Background(), e.org.ID, inc, alerts); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("IncidentChanged blocked %v", time.Since(start))
	}
	rows := e.outbound(t)
	if !hasBody(rows, store.ChannelWeb, "OOM kill") {
		t.Fatalf("rule notification missing: %+v", rows)
	}
	if hasBody(rows, store.ChannelWeb, "Haetae:") {
		t.Fatal("report posted while operator blocked")
	}
}

func TestIncidentReportPostedAfterRuleMessageAndDebounced(t *testing.T) {
	e := newOpEnv(t, nil)
	op := &fakeOp{avail: true, answer: sampleAnswer(e.host.ID)}
	e.svc.SetOperator(op)
	inc, alerts := incidentFor(t, e)
	ctx := context.Background()
	if err := e.svc.IncidentChanged(ctx, e.org.ID, inc, alerts); err != nil {
		t.Fatal(err)
	}
	rows := e.waitFor(t, "report", func(r []outRow) bool { return hasBody(r, store.ChannelTelegram, "Haetae: ") })
	if rows[0].Kind != store.KindIncident {
		t.Fatalf("first message is %s, want the rule notification", rows[0].Kind)
	}
	// Same incident again within 10 minutes: no second report.
	if err := e.svc.IncidentChanged(ctx, e.org.ID, inc, alerts); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := op.explained.Load(); n != 1 {
		t.Fatalf("ExplainIncident called %d times", n)
	}
}

func TestIncidentReportCauseNotFoundAndFailure(t *testing.T) {
	e := newOpEnv(t, nil)
	e.svc.SetOperator(&fakeOp{avail: true, answer: operator.Answer{CauseNotFound: true}})
	inc, alerts := incidentFor(t, e)
	if err := e.svc.IncidentChanged(context.Background(), e.org.ID, inc, alerts); err != nil {
		t.Fatal(err)
	}
	e.waitFor(t, "cause not found", func(r []outRow) bool {
		return hasBody(r, store.ChannelWeb, "Haetae: cause not found for api down")
	})

	e2 := newOpEnv(t, &fakeOp{avail: true, askErr: errors.New("boom")})
	inc2, alerts2 := incidentFor(t, e2)
	if err := e2.svc.IncidentChanged(context.Background(), e2.org.ID, inc2, alerts2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if hasBody(e2.outbound(t), store.ChannelWeb, "Haetae:") {
		t.Fatal("failure must post nothing")
	}
}
