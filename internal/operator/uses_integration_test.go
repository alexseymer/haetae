//go:build integration

package operator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/store"
)

// openIncident creates an incident of org one with one firing alert.
func (e *env) openIncident(rule, container, summary string) store.Incident {
	e.t.Helper()
	ctx := context.Background()
	inc, _, err := e.s.OpenIncidentForHost(ctx, e.org.ID, e.host.ID, "incident on "+container, "high")
	if err != nil {
		e.t.Fatal(err)
	}
	if _, _, err := e.s.UpsertFiringAlert(ctx, store.Alert{OrgID: e.org.ID, HostID: e.host.ID, IncidentID: inc.ID, Rule: rule,
		Container: container, Severity: "high", DedupeKey: "k-" + rule + container, Summary: summary, Evidence: json.RawMessage(`{}`)}); err != nil {
		e.t.Fatal(err)
	}
	return inc
}

func TestExplainIncidentWithEvidence(t *testing.T) {
	e := newEnv(t)
	inc := e.openIncident("crash_loop", "db", "db keeps restarting")
	f := fakeOf(final(`{"statements":[{"text":"db is crash looping","evidence":["ev_1"]},{"text":"guess","evidence":[]}]}`))
	ans, err := e.svc(f).ExplainIncident(context.Background(), e.org.ID, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ans.CauseNotFound || len(ans.Statements) != 1 || ans.DroppedCount != 1 || len(ans.Evidence) != 1 {
		t.Fatalf("ans = %+v", ans)
	}
	ev := ans.Evidence[0]
	if !ev.Trusted || ev.Kind != "events" || ev.Link != "/incidents/"+inc.ID.String() {
		t.Fatalf("evidence = %+v", ev)
	}
	stored, err := e.s.GetEvidence(context.Background(), e.org.ID, ans.ConversationID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	// The seed reaches the model as a tool result with third-party text only
	// under "untrusted".
	req := f.Requests()[0]
	if len(req.Messages) != 3 || req.Messages[0].Text == "" || req.Messages[2].Role != provider.RoleTool {
		t.Fatalf("messages = %+v", req.Messages)
	}
	var res map[string]json.RawMessage
	if err := json.Unmarshal([]byte(req.Messages[2].Text), &res); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res["data"]), "keeps restarting") || !strings.Contains(string(res["untrusted"]), "keeps restarting") {
		t.Fatalf("result = %s", req.Messages[2].Text)
	}
	if strings.Contains(req.System, "keeps restarting") || strings.Contains(req.Messages[0].Text, "keeps restarting") {
		t.Fatal("alert text outside the tool result")
	}
}

func TestExplainIncidentWithoutSupportIsCauseNotFound(t *testing.T) {
	e := newEnv(t)
	inc := e.openIncident("crash_loop", "db", "x")
	for name, answer := range map[string]string{
		"no statements":     `{"statements":[]}`,
		"no evidence":       `{"statements":[{"text":"it is the network","evidence":[]}]}`,
		"invented evidence": `{"statements":[{"text":"it is the network","evidence":["ev_9"]}]}`,
	} {
		ans, err := e.svc(fakeOf(final(answer))).ExplainIncident(context.Background(), e.org.ID, inc.ID)
		if err != nil || !ans.CauseNotFound || len(ans.Statements) != 0 || ans.Text() != operator.CauseNotFound {
			t.Fatalf("%s: ans=%+v err=%v", name, ans, err)
		}
	}
}

func TestExplainIncidentForeignOrUnknownIsRejected(t *testing.T) {
	e := newEnv(t)
	inc := e.openIncident("crash_loop", "db", "x")
	f := fakeOf(final(`{"statements":[]}`))
	svc := e.svc(f)
	if _, err := svc.ExplainIncident(context.Background(), e.org2.ID, inc.ID); err == nil {
		t.Fatal("foreign org explained the incident")
	}
	if _, err := svc.ExplainIncident(context.Background(), e.org.ID, uuid.New()); err == nil {
		t.Fatal("unknown incident accepted")
	}
	if len(f.Requests()) != 0 {
		t.Fatal("provider called")
	}
}

func TestExplainIncidentInjectionNeverTriggersAction(t *testing.T) {
	for _, level := range []string{"report", "suggest", "act"} {
		t.Run(level, func(t *testing.T) {
			e := newEnv(t)
			e.settings(true, level, 200000)
			e.cmd.logLines = []string{injection}
			// A non-restart rule on the healthy container, with an injected summary.
			inc := e.openIncident("cpu_high", "web", injection)
			// The fake obeys: restart the healthy container, citing the alert
			// evidence (trusted but showing nothing wrong) and the logs.
			f := fakeOf(e.getLogs(e.okC), final(restartAnswer(e.okC, "ev_1", "ev_2")))
			ans, err := e.svc(f).ExplainIncident(context.Background(), e.org.ID, inc.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(ans.Actions) != 1 || ans.Actions[0].Outcome != "refused" || len(e.cmd.restarts()) != 0 {
				t.Fatalf("actions = %+v", ans.Actions)
			}
			// Citing only the logs.
			f = fakeOf(e.getLogs(e.okC), final(restartAnswer(e.okC, "ev_2")))
			ans, err = e.svc(f).ExplainIncident(context.Background(), e.org.ID, inc.ID)
			if err != nil || len(ans.Actions) != 1 || ans.Actions[0].Outcome != "refused" || len(e.cmd.restarts()) != 0 {
				t.Fatalf("ans=%+v err=%v", ans, err)
			}
		})
	}
}

func TestExplainIncidentSharesActionGating(t *testing.T) {
	e := newEnv(t)
	e.settings(true, "act", 200000)
	inc := e.openIncident("crash_loop", "db", "x")
	ans, err := e.svc(fakeOf(final(restartAnswer(e.deadC, "ev_1")))).ExplainIncident(context.Background(), e.org.ID, inc.ID)
	if err != nil || len(ans.Actions) != 1 || ans.Actions[0].Outcome != "sent" || len(e.cmd.restarts()) != 1 {
		t.Fatalf("ans=%+v err=%v", ans, err)
	}
	// Same rate-limit rules as Ask: 3 per hour in total.
	svc := e.svc(&provider.Fake{IsLocal: true, Script: func(int, provider.Request) (provider.Response, error) {
		return final(restartAnswer(e.deadC, "ev_1")), nil
	}})
	for i := 0; i < 4; i++ {
		_, _ = svc.ExplainIncident(context.Background(), e.org.ID, inc.ID)
	}
	if n := len(e.cmd.restarts()); n != 3 {
		t.Fatalf("restarts = %d, want 3", n)
	}
}

func TestExplainResolvedIncidentIgnoresActions(t *testing.T) {
	e := newEnv(t)
	e.settings(true, "act", 200000)
	inc := e.openIncident("crash_loop", "db", "x")
	if _, err := e.s.ResolveAlert(context.Background(), e.org.ID, "k-crash_loopdb"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ResolveIncident(context.Background(), e.org.ID, inc.ID); err != nil {
		t.Fatal(err)
	}
	ans, err := e.svc(fakeOf(final(restartAnswer(e.deadC, "ev_1")))).ExplainIncident(context.Background(), e.org.ID, inc.ID)
	if err != nil || len(ans.Statements) != 1 || len(ans.Actions) != 0 || len(e.cmd.restarts()) != 0 {
		t.Fatalf("ans=%+v err=%v", ans, err)
	}
	if len(e.actions()) != 0 {
		t.Fatal("a proposal was recorded")
	}
}

func TestExplainIncidentCapAndProviderError(t *testing.T) {
	e := newEnv(t)
	inc := e.openIncident("crash_loop", "db", "x")
	e.settings(true, "report", 100)
	if err := e.s.AddUsage(context.Background(), e.org.ID, time.Now(), 60, 40); err != nil {
		t.Fatal(err)
	}
	f := fakeOf(final(`{}`))
	if _, err := e.svc(f).ExplainIncident(context.Background(), e.org.ID, inc.ID); !errors.Is(err, operator.ErrCapped) || len(f.Requests()) != 0 {
		t.Fatalf("err = %v", err)
	}
	e.settings(true, "report", 200000)
	bad := &provider.Fake{IsLocal: true, Script: func(int, provider.Request) (provider.Response, error) {
		return provider.Response{}, provider.ErrUnavailable
	}}
	if _, err := e.svc(bad).ExplainIncident(context.Background(), e.org.ID, inc.ID); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// ---- briefing ----

var briefingFacts = operator.BriefingFacts{Hosts: 2, Containers: 14, Running: 13, Items: []string{"db-1 down"}}

const briefingRule = "2 hosts, 14 containers, 13 running. db-1 down"

func textJSON(s string) provider.Response {
	b, _ := json.Marshal(map[string]string{"text": s})
	return final(string(b))
}

func TestRewriteBriefingAccepted(t *testing.T) {
	e := newEnv(t)
	f := fakeOf(textJSON("2 hosts, 14 containers (13 running).\ndb-1 down"))
	got, err := e.svc(f).RewriteBriefing(context.Background(), e.org.ID, briefingFacts, briefingRule)
	if err != nil || got != "2 hosts, 14 containers (13 running).\ndb-1 down" {
		t.Fatalf("got %q err %v", got, err)
	}
	req := f.Requests()[0]
	if len(req.Tools) != 0 || len(req.Messages) != 1 {
		t.Fatalf("request = %+v", req)
	}
	var payload struct {
		Facts     map[string]any
		Untrusted struct {
			Items    []string `json:"items"`
			RuleText string   `json:"rule_text"`
		}
	}
	if err := json.Unmarshal([]byte(req.Messages[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Untrusted.Items) != 1 || payload.Untrusted.Items[0] != "db-1 down" || payload.Untrusted.RuleText != briefingRule {
		t.Fatalf("payload = %s", req.Messages[0].Text)
	}
	facts, _ := json.Marshal(payload.Facts)
	if strings.Contains(string(facts), "db-1") || strings.Contains(req.System, "db-1") {
		t.Fatal("item outside the untrusted key")
	}
	if ok, _ := e.s.UsageToday(context.Background(), e.org.ID, time.Now()); ok.Total() == 0 {
		t.Fatal("usage not recorded")
	}
}

func TestRewriteBriefingRejections(t *testing.T) {
	e := newEnv(t)
	cases := map[string]provider.Response{
		"four lines":     textJSON("2 hosts\n14 containers\n13 running\ndb-1 down"),
		"too long":       textJSON("2 hosts, 14 containers, 13 running. db-1 down " + strings.Repeat("z", 300)),
		"missing item":   textJSON("2 hosts, 14 containers, 13 running. all fine"),
		"missing number": textJSON("2 hosts, 14 containers. db-1 down"),
		"control":        textJSON("2 hosts, 14 containers, 13 running.\x1b[2J db-1 down"),
		"not json":       final("2 hosts, 14 containers, 13 running. db-1 down"),
		"unknown field":  final(`{"text":"2 hosts, 14 containers, 13 running. db-1 down","x":1}`),
		"wrong type":     final(`{"text":5}`),
		"empty":          textJSON(""),
	}
	for name, resp := range cases {
		got, err := e.svc(fakeOf(resp)).RewriteBriefing(context.Background(), e.org.ID, briefingFacts, briefingRule)
		if err == nil || got != "" {
			t.Errorf("%s: got %q err %v", name, got, err)
		}
	}
	bad := &provider.Fake{IsLocal: true, Script: func(int, provider.Request) (provider.Response, error) {
		return provider.Response{}, provider.ErrUnavailable
	}}
	if _, err := e.svc(bad).RewriteBriefing(context.Background(), e.org.ID, briefingFacts, briefingRule); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	// Facts with no items and an empty rule text never panic.
	if _, err := e.svc(fakeOf(textJSON("0 hosts, 0 containers, 0 running"))).RewriteBriefing(context.Background(), e.org.ID, operator.BriefingFacts{}, ""); err != nil {
		t.Fatalf("empty facts: %v", err)
	}
}

func TestRewriteBriefingCap(t *testing.T) {
	e := newEnv(t)
	e.settings(true, "report", 100)
	if err := e.s.AddUsage(context.Background(), e.org.ID, time.Now(), 60, 40); err != nil {
		t.Fatal(err)
	}
	f := fakeOf(textJSON("x"))
	if _, err := e.svc(f).RewriteBriefing(context.Background(), e.org.ID, briefingFacts, briefingRule); !errors.Is(err, operator.ErrCapped) || len(f.Requests()) != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestRewriteBriefingNonLocalNeverSeesEmailsOrSecrets(t *testing.T) {
	e := newEnv(t)
	facts := operator.BriefingFacts{Hosts: 1, Containers: 1, Running: 0, Items: []string{"bob@example.com-app down", "key AKIAABCDEFGHIJKLMNOP"}}
	f := &provider.Fake{Responses: []provider.Response{textJSON("1 hosts")}}
	_, _ = e.svc(f).RewriteBriefing(context.Background(), e.org.ID, facts, "bob@example.com-app down; key AKIAABCDEFGHIJKLMNOP")
	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	got := reqs[0].System + reqs[0].Messages[0].Text
	if strings.Contains(got, "bob@example.com") || strings.Contains(got, "AKIAABCDEFGHIJKLMNOP") {
		t.Fatal("a secret or email reached the non-local provider")
	}
}

func TestRewriteBriefingRedactionFailureSendsNothing(t *testing.T) {
	e := newEnv(t)
	f := &provider.Fake{Responses: []provider.Response{textJSON("x")}}
	svc := e.svc(f, func(c *operator.Config) {
		c.Redact = func(string) (string, error) { return "", errors.New("boom") }
	})
	if _, err := svc.RewriteBriefing(context.Background(), e.org.ID, briefingFacts, briefingRule); !errors.Is(err, operator.ErrRedaction) || len(f.Requests()) != 0 {
		t.Fatalf("err = %v", err)
	}
}

// ---- judge ----

func verdictsJSON(vs ...string) provider.Response {
	return final(`{"verdicts":[` + strings.Join(vs, ",") + `]}`)
}

func tq(id string, level int16, tpl string) operator.TemplateQuery {
	return operator.TemplateQuery{TemplateID: id, Level: level, Template: tpl, Count: 7}
}

func TestJudgeTemplatesNeverSendsOrReturnsAboveInfo(t *testing.T) {
	e := newEnv(t)
	ts := []operator.TemplateQuery{
		tq("t-info", store.LevelInfo, "started <*>"),
		tq("t-warn", store.LevelWarn, "WARNTEXT <*>"),
		tq("t-err", store.LevelError, "ERRTEXT <*>"),
		tq("t-fatal", store.LevelFatal, "FATALTEXT"),
		tq("t-unknown", 0, "UNKNOWNTEXT"),
		tq("t-debug", store.LevelDebug, "cache hit <*>"),
	}
	// The fake says everything is green, including the ones it must not see.
	f := fakeOf(verdictsJSON(
		`{"id":"t-info","green":true}`, `{"id":"t-warn","green":true}`, `{"id":"t-err","green":true}`,
		`{"id":"t-fatal","green":true}`, `{"id":"t-unknown","green":true}`, `{"id":"t-debug","green":false}`))
	got, err := e.svc(f).JudgeTemplates(context.Background(), e.org.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (operator.Verdict{TemplateID: "t-info", Green: true}) || got[1] != (operator.Verdict{TemplateID: "t-debug", Green: false}) {
		t.Fatalf("got = %+v", got)
	}
	req := f.Requests()[0]
	sent := req.System + req.Messages[0].Text
	for _, bad := range []string{"WARNTEXT", "ERRTEXT", "FATALTEXT", "UNKNOWNTEXT", "t-warn", "t-err", "t-fatal", "t-unknown"} {
		if strings.Contains(sent, bad) {
			t.Fatalf("%q was sent", bad)
		}
	}
	// Template text only under "untrusted".
	var p struct {
		Templates []map[string]json.RawMessage `json:"templates"`
	}
	if err := json.Unmarshal([]byte(req.Messages[0].Text), &p); err != nil || len(p.Templates) != 2 {
		t.Fatalf("payload %s err %v", req.Messages[0].Text, err)
	}
	for _, tpl := range p.Templates {
		if _, ok := tpl["template"]; ok || !strings.Contains(string(tpl["untrusted"]), "template") {
			t.Fatalf("template outside untrusted: %v", tpl)
		}
	}
	if strings.Contains(req.System, "started") || len(req.Tools) != 0 {
		t.Fatal("unexpected system text or tools")
	}
	// Only above-info templates: nothing is sent at all.
	f2 := fakeOf(verdictsJSON())
	got, err = e.svc(f2).JudgeTemplates(context.Background(), e.org.ID, ts[1:5])
	if err != nil || len(got) != 0 || len(f2.Requests()) != 0 {
		t.Fatalf("got=%v err=%v reqs=%d", got, err, len(f2.Requests()))
	}
}

func TestJudgeTemplatesBatchUnknownOmittedAndDuplicates(t *testing.T) {
	e := newEnv(t)
	var ts []operator.TemplateQuery
	for i := 0; i < 60; i++ {
		ts = append(ts, tq(fmt.Sprintf("t%02d", i), store.LevelInfo, "line"))
	}
	ts = append(ts, tq("t00", store.LevelInfo, "dup")) // duplicate of the first
	f := fakeOf(verdictsJSON(
		`{"id":"t01","green":true}`,
		`{"id":"t01","green":false}`, // second answer for the same id
		`{"id":"nope","green":true}`, // not in the batch
		`{"id":"t55","green":true}`,  // beyond the batch of 50
		`{"id":"t02"}`,               // no explicit verdict
		`{"id":"t03","green":false}`))
	got, err := e.svc(f).JudgeTemplates(context.Background(), e.org.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (operator.Verdict{TemplateID: "t01", Green: true}) || got[1] != (operator.Verdict{TemplateID: "t03"}) {
		t.Fatalf("got = %+v", got)
	}
	var p struct {
		Templates []struct{ ID string } `json:"templates"`
	}
	if err := json.Unmarshal([]byte(f.Requests()[0].Messages[0].Text), &p); err != nil || len(p.Templates) != 50 || p.Templates[0].ID != "t00" {
		t.Fatalf("batch = %d err %v", len(p.Templates), err)
	}
}

func TestJudgeTemplatesFailures(t *testing.T) {
	e := newEnv(t)
	ts := []operator.TemplateQuery{tq("a", store.LevelInfo, "x")}
	for name, in := range map[string]string{
		"not json":   "green",
		"unknown":    `{"verdicts":[],"extra":1}`,
		"wrong type": `{"verdicts":[{"id":"a","green":"yes"}]}`,
		"truncated":  `{"verdicts":[{"id":"a"`,
	} {
		got, err := e.svc(fakeOf(final(in))).JudgeTemplates(context.Background(), e.org.ID, ts)
		if !errors.Is(err, operator.ErrBadResponse) || got != nil {
			t.Errorf("%s: got=%v err=%v", name, got, err)
		}
	}
	e.settings(true, "report", 100)
	if err := e.s.AddUsage(context.Background(), e.org.ID, time.Now(), 60, 40); err != nil {
		t.Fatal(err)
	}
	f := fakeOf(verdictsJSON())
	if _, err := e.svc(f).JudgeTemplates(context.Background(), e.org.ID, ts); !errors.Is(err, operator.ErrCapped) || len(f.Requests()) != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestJudgeTemplatesNonLocalRedacts(t *testing.T) {
	e := newEnv(t)
	f := &provider.Fake{Responses: []provider.Response{verdictsJSON()}}
	_, err := e.svc(f).JudgeTemplates(context.Background(), e.org.ID, []operator.TemplateQuery{tq("a", store.LevelInfo, "mail to bob@example.com")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.Requests()[0].Messages[0].Text, "bob@example.com") {
		t.Fatal("email reached the non-local provider")
	}
}
