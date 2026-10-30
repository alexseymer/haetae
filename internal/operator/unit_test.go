package operator

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/store"
)

func mkConv(evs ...*evRec) *conv { return &conv{evs: evs} }

func ev(id, kind string, trusted bool, data string) *evRec {
	return &evRec{Evidence: Evidence{ID: id, Kind: kind, Trusted: trusted, Data: json.RawMessage(data)}}
}

func TestValidateDropsStatementsWithoutValidEvidence(t *testing.T) {
	c := mkConv(ev("ev_1", "hosts", true, `{}`), ev("ev_2", "containers", true, `{}`), ev("ev_3", "metrics", true, `{}`))
	kept, dropped, cited := validate(c, []wireStatement{
		{Text: "supported", Evidence: []string{"ev_2"}},
		{Text: "no evidence", Evidence: nil},
		{Text: "foreign id", Evidence: []string{"ev_9"}},
		{Text: "mixed valid and foreign", Evidence: []string{"ev_1", "ev_77"}},
		{Text: "bad syntax", Evidence: []string{"../ev_1"}},
		{Text: "also supported [ev_1]", Evidence: []string{"ev_1", "ev_1"}},
		{Text: "   ", Evidence: []string{"ev_1"}},
	})
	if len(kept) != 2 || dropped != 5 {
		t.Fatalf("kept=%v dropped=%d", kept, dropped)
	}
	if kept[1].Text != "also supported" {
		t.Fatalf("invented marker not stripped: %q", kept[1].Text)
	}
	if len(cited) != 2 || cited[0].ID != "ev_1" || cited[1].ID != "ev_2" {
		t.Fatalf("cited = %+v (want ev_1, ev_2 only, in order)", cited)
	}
}

func TestStatementTextIsCleanedAndLimited(t *testing.T) {
	c := mkConv(ev("ev_1", "hosts", true, `{}`))
	long := strings.Repeat("ä", 900)
	kept, _, _ := validate(c, []wireStatement{{Text: "a\x00b\nc\u202ed [ev_1 [ev_ " + long, Evidence: []string{"ev_1"}}})
	if len(kept) != 1 {
		t.Fatal("dropped")
	}
	got := kept[0].Text
	if strings.ContainsAny(got, "\x00\n\u202e") || strings.Contains(got, "[ev_") {
		t.Fatalf("not cleaned: %q", got)
	}
	if n := len([]rune(got)); n > maxStatementLen {
		t.Fatalf("%d runes", n)
	}
}

func TestParseAnswerIsStrict(t *testing.T) {
	good := `Sure! {"statements":[{"text":"x","evidence":["ev_1"]}],"actions":[]} trailing {junk`
	if w, err := parseAnswer(good); err != nil || len(w.Statements) != 1 {
		t.Fatalf("good: %v %+v", err, w)
	}
	for name, in := range map[string]string{
		"unknown field":  `{"statements":[],"extra":1}`,
		"unknown nested": `{"statements":[{"text":"x","evidence":[],"score":1}]}`,
		"wrong type":     `{"statements":"nope"}`,
		"no object":      `just prose`,
		"truncated":      `{"statements":[`,
		"oversize":       `{"statements":[{"text":"` + strings.Repeat("a", 33<<10) + `","evidence":[]}]}`,
	} {
		if _, err := parseAnswer(in); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestEvidenceShowsUnhealthy(t *testing.T) {
	containers := func(state, health string) string {
		return `{"data":{"containers":[{"container_id":"c1","state":"` + state + `","health":"` + health + `"},{"container_id":"c2","state":"running","health":"healthy"}]}}`
	}
	alerts := `{"data":{"alerts":[{"rule":"crash_loop","state":"firing","container_id":"c1"},{"rule":"cpu_high","state":"firing","container_id":"c2"},{"rule":"unhealthy","state":"resolved","container_id":"c3"}]}}`
	cases := []struct {
		name string
		ev   Evidence
		id   string
		want bool
	}{
		{"exited", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(containers("exited", ""))}, "c1", true},
		{"restarting", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(containers("restarting", ""))}, "c1", true},
		{"unhealthy", Evidence{Kind: "findings", Trusted: true, Data: json.RawMessage(containers("running", "unhealthy"))}, "c1", true},
		{"running healthy", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(containers("running", "healthy"))}, "c1", false},
		{"other container", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(containers("exited", ""))}, "c2", false},
		{"prefix is not equal", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(containers("exited", ""))}, "c", false},
		{"untrusted flag", Evidence{Kind: "containers", Trusted: false, Data: json.RawMessage(containers("exited", ""))}, "c1", false},
		{"logs never", Evidence{Kind: "logs", Trusted: true, Data: json.RawMessage(containers("exited", ""))}, "c1", false},
		{"metrics kind", Evidence{Kind: "metrics", Trusted: true, Data: json.RawMessage(containers("exited", ""))}, "c1", false},
		{"crash loop alert", Evidence{Kind: "events", Trusted: true, Data: json.RawMessage(alerts)}, "c1", true},
		{"unrelated alert", Evidence{Kind: "events", Trusted: true, Data: json.RawMessage(alerts)}, "c2", false},
		{"resolved alert", Evidence{Kind: "events", Trusted: true, Data: json.RawMessage(alerts)}, "c3", false},
		{"untrusted section ignored", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(`{"untrusted":{"containers":[{"container_id":"c1","state":"exited"}]}}`)}, "c1", false},
		{"garbage", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(`not json`)}, "c1", false},
		{"empty id", Evidence{Kind: "containers", Trusted: true, Data: json.RawMessage(containers("exited", ""))}, "", false},
	}
	for _, tc := range cases {
		if got := evidenceShowsUnhealthy(tc.ev, tc.id); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestEgressRedactsForNonLocalOnly(t *testing.T) {
	red := defaultRedact()
	remote := &provider.Fake{}
	in := []string{"contact bob@example.com", "key AKIAABCDEFGHIJKLMNOP", "fine"}
	out, err := Egress(context.Background(), remote, red, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out[0], "bob@example.com") || strings.Contains(out[1], "AKIAABCDEFGHIJKLMNOP") || out[2] != "fine" {
		t.Fatalf("out = %q", out)
	}
	local := &provider.Fake{IsLocal: true}
	out, err = Egress(context.Background(), local, red, in)
	if err != nil || out[0] != in[0] {
		t.Fatalf("local provider text changed: %q %v", out, err)
	}
}

func TestEgressFailsClosed(t *testing.T) {
	remote := &provider.Fake{}
	boom := func(s string) (string, error) {
		if strings.Contains(s, "second") {
			return "", errors.New("boom")
		}
		return s, nil
	}
	out, err := Egress(context.Background(), remote, boom, []string{"first", "second"})
	if !errors.Is(err, ErrRedaction) || out != nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
	pan := func(string) (string, error) { panic("x") }
	if _, err := Egress(context.Background(), remote, pan, []string{"a"}); !errors.Is(err, ErrRedaction) {
		t.Fatalf("panic err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Egress(ctx, remote, defaultRedact(), []string{"a"}); !errors.Is(err, ErrRedaction) {
		t.Fatalf("cancelled err = %v", err)
	}
	if _, err := Egress(context.Background(), remote, nil, []string{"a"}); !errors.Is(err, ErrRedaction) {
		t.Fatalf("nil redactor err = %v", err)
	}
}

func TestParseWindowAndIDs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	f := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	if _, _, err := parseWindow(f(-24*time.Hour), f(0)); err != nil {
		t.Fatalf("exactly 24h: %v", err)
	}
	for name, p := range map[string][2]string{
		"over 24h":   {f(-25 * time.Hour), f(0)},
		"reversed":   {f(0), f(-time.Hour)},
		"equal":      {f(0), f(0)},
		"not a time": {"yesterday", f(0)},
	} {
		if _, _, err := parseWindow(p[0], p[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a;rm", strings.Repeat("a", 129), "../x"} {
		if checkContainerID(bad) == nil {
			t.Errorf("container id %q accepted", bad)
		}
	}
	if checkContainerID("a1b2c3d4e5f6") != nil {
		t.Error("valid id rejected")
	}
}

func TestAvailableWithoutProvider(t *testing.T) {
	s := New(Config{})
	ok, reason := s.Available(context.Background(), [16]byte{1})
	if ok || reason != "not configured" {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
	if _, err := s.Ask(context.Background(), [16]byte{1}, "q"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Ask err = %v", err)
	}
}

// S9: the rule-based alerts and the operator must not depend on each other.
func TestAlertsAndOperatorDoNotImportEachOther(t *testing.T) {
	const (
		opPath     = "github.com/alexseymer/haetae/internal/operator"
		alertsPath = "github.com/alexseymer/haetae/internal/alerts"
	)
	check := func(dir, banned string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		fset := token.NewFileSet()
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			n++
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if p == banned || strings.HasPrefix(p, banned+"/") {
					t.Errorf("%s/%s imports %s", dir, e.Name(), p)
				}
			}
		}
		if n == 0 {
			t.Fatalf("no Go files in %s", dir)
		}
	}
	check(".", alertsPath)
	check("../alerts", opPath)
}

func TestEvidenceTitleUsesNamesNotIDs(t *testing.T) {
	hostID := uuid.MustParse("b3825172-abd9-421b-bbf9-c8c828868297")
	h := store.Host{ID: hostID, Name: "hel1-prod"}
	title := "Containers of " + hostLabel(h)
	if !strings.Contains(title, "hel1-prod") || strings.Contains(title, hostID.String()) {
		t.Fatalf("host title %q must name the host and not carry its UUID", title)
	}

	cid := "1b60a1b5f36ea2ef2f22daccec57c58e01e397d1e7d796f1cea08a008c38293f"
	if got := containerLabel(store.Container{ContainerID: cid, Name: "shop-api"}); got != "shop-api" {
		t.Errorf("named container label %q", got)
	}
	if got := containerLabel(store.Container{ContainerID: cid}); got != cid[:12] {
		t.Errorf("unnamed container label %q, want first 12 id characters", got)
	}
	if got := displayName("sh\x00op\n-api\x1b[31m"); got != "shop-api[31m" {
		t.Errorf("control characters not stripped: %q", got)
	}
	if got := displayName(strings.Repeat("é", 100)); utf8.RuneCountInString(got) != 60 {
		t.Errorf("name not limited to 60 runes: %d", utf8.RuneCountInString(got))
	}
}

// TestAskContainerPayloadSurvivesEgress builds the exact JSON a list_containers
// result hands to a remote model (modelPayload after containerData and
// containerUntrusted) and runs it through the real egress redaction. The
// placeholders must stay one per value, with no identifier turned into a
// card-number token that loses its context.
func TestAskContainerPayloadSurvivesEgress(t *testing.T) {
	for _, hostID := range []uuid.UUID{
		uuid.MustParse("3f2a9c1e-7b4d-4e8a-9c21-5d6e7f8a9b0c"),
		uuid.MustParse("12345678-1234-4123-8123-123456789012"),
		uuid.Nil,
	} {
		now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
		cs := []store.Container{
			{HostID: hostID, ContainerID: "9f3c1a7b2e4d5c6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f", Name: "shop-web",
				Stack: "shop", Service: "web", Image: "ghcr.io/acme/shop-web:1.4.2", State: "running", Status: "Up 3 hours (healthy)", Health: "healthy", UpdatedAt: now},
			{HostID: hostID, ContainerID: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a", Name: "shop-worker",
				Stack: "shop", Service: "worker", Image: "ghcr.io/acme/shop-worker:1.4.2", State: "exited", Status: "Exited (1) 2 minutes ago", ExitCode: 1, UpdatedAt: now},
		}
		data := make([]map[string]any, 0, len(cs))
		unt := make([]map[string]any, 0, len(cs))
		for _, c := range cs {
			data = append(data, containerData(c))
			unt = append(unt, containerUntrusted(c))
		}
		out := &toolOut{trusted: true, data: map[string]any{"containers": data, "total": len(cs)}, untrusted: map[string]any{"containers": unt}}
		payload := modelPayload("ev_1", out)

		red, err := Egress(context.Background(), &provider.Fake{}, defaultRedact(), []string{payload})
		if err != nil {
			t.Fatal(err)
		}
		got := red[0]
		if strings.Contains(got, "][") {
			t.Fatalf("adjacent placeholders (host %s): %s", hostID, got)
		}
		if strings.Contains(got, "credit-card") {
			t.Fatalf("identifier redacted as a card number (host %s): %s", hostID, got)
		}
		if !strings.Contains(got, hostID.String()) {
			t.Fatalf("host id altered (host %s): %s", hostID, got)
		}
		if !strings.Contains(got, "shop-web") || !strings.Contains(got, "Up 3 hours (healthy)") {
			t.Fatalf("container fields lost: %s", got)
		}
	}
}
