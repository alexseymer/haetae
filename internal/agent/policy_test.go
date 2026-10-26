package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/gen/haetae/agent/v1/agentv1connect"
)

func kindCommand(kind string, auth agentv1.Authorization) *agentv1.Command {
	c := &agentv1.Command{Id: "c-" + kind, Authorization: auth}
	switch kind {
	case "ping":
		c.Kind = &agentv1.Command_Ping{Ping: &agentv1.Ping{}}
	case "query_logs":
		c.Kind = &agentv1.Command_QueryLogs{QueryLogs: &agentv1.QueryLogs{}}
	case "deploy":
		c.Kind = &agentv1.Command_Deploy{Deploy: &agentv1.Deploy{}}
	case "remove_project":
		c.Kind = &agentv1.Command_RemoveProject{RemoveProject: &agentv1.RemoveProject{}}
	case "scan_container":
		c.Kind = &agentv1.Command_ScanContainer{ScanContainer: &agentv1.ScanContainer{}}
	case "scan_image":
		c.Kind = &agentv1.Command_ScanImage{ScanImage: &agentv1.ScanImage{}}
	case "pull_image":
		c.Kind = &agentv1.Command_PullImage{PullImage: &agentv1.PullImage{}}
	case "restart_container":
		c.Kind = &agentv1.Command_RestartContainer{RestartContainer: &agentv1.RestartContainer{ContainerId: "0123456789ab"}}
	}
	return c
}

func TestPolicyTable(t *testing.T) {
	const (
		U = agentv1.Authorization_AUTHORIZATION_UNSPECIFIED
		P = agentv1.Authorization_AUTHORIZATION_APPROVED
		A = agentv1.Authorization_AUTHORIZATION_AUTO
	)
	for _, tc := range []struct {
		announced, cap string
		auth           agentv1.Authorization
		kind           string
		want           string // "" allowed, else substring of the refusal
	}{
		{"report", "", A, "restart_container", "autonomy level report does not allow restart_container (auto)"},
		{"report", "", P, "restart_container", "level report does not allow restart_container (approved)"},
		{"report", "", U, "restart_container", "operator command needs an authorization"},
		{"suggest", "", A, "restart_container", "level suggest does not allow restart_container (auto)"},
		{"suggest", "", P, "restart_container", ""},
		{"suggest", "", U, "restart_container", "needs an authorization"},
		{"act", "", A, "restart_container", ""},
		{"act", "", P, "restart_container", ""},
		{"act", "", U, "restart_container", "needs an authorization"},
		{"act", "report", A, "restart_container", "level report does not allow"},
		{"act", "suggest", A, "restart_container", "level suggest does not allow"},
		{"act", "suggest", P, "restart_container", ""},
		{"report", "act", P, "restart_container", "level report"},
		{"bogus", "", A, "restart_container", "level report"},
		{"", "", P, "restart_container", "level report"},
		{"act", "bogus", A, "restart_container", "level report"},
	} {
		p := NewPolicy(tc.cap, nil)
		p.SetServerLevel(tc.announced)
		got := p.Check(kindCommand(tc.kind, tc.auth))
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("announced=%q cap=%q auth=%v kind=%s: got %q, want %q", tc.announced, tc.cap, tc.auth, tc.kind, got, tc.want)
		}
	}
	// User-class kinds are allowed at every level and authorization.
	for _, kind := range commandKinds(t) {
		if commandClasses[kind] != ClassUser {
			continue
		}
		for _, lvl := range []string{"report", "suggest", "act", "weird"} {
			for _, auth := range []agentv1.Authorization{U, P, A} {
				p := NewPolicy("", nil)
				p.SetServerLevel(lvl)
				if got := p.Check(kindCommand(kind, auth)); got != "" {
					t.Errorf("%s at %s/%v refused: %s", kind, lvl, auth, got)
				}
			}
		}
	}
	// No kind at all is unsupported.
	if got := NewPolicy("", nil).Check(&agentv1.Command{Id: "x"}); got != "unsupported command" {
		t.Errorf("empty command: %q", got)
	}
}

func TestParseMaxAutonomy(t *testing.T) {
	for _, ok := range []string{"report", "suggest", "act"} {
		if _, err := ParseMaxAutonomy(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ACT", "root", "act "} {
		if _, err := ParseMaxAutonomy(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakeGateway is a scripted server side of the Connect stream.
type fakeGateway struct {
	agentv1connect.UnimplementedAgentGatewayServiceHandler
	welcomeLevel string
	out          chan *agentv1.ConnectResponse
	results      chan *agentv1.CommandResult
}

func (f *fakeGateway) Connect(ctx context.Context, st *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]) error {
	if _, err := st.Receive(); err != nil { // hello
		return err
	}
	if err := st.Send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Welcome{Welcome: &agentv1.Welcome{HeartbeatSeconds: 3600, Autonomy: f.welcomeLevel}}}); err != nil {
		return err
	}
	go func() {
		for {
			m, err := st.Receive()
			if err != nil {
				return
			}
			if r := m.GetResult(); r != nil {
				f.results <- r
			}
		}
	}()
	for {
		select {
		case m := <-f.out:
			if err := st.Send(m); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

type sessionHarness struct {
	gw       *fakeGateway
	restarts chan string
}

func startSession(t *testing.T, welcome, localCap string) *sessionHarness {
	t.Helper()
	h := &sessionHarness{
		gw:       &fakeGateway{welcomeLevel: welcome, out: make(chan *agentv1.ConnectResponse, 8), results: make(chan *agentv1.CommandResult, 8)},
		restarts: make(chan string, 8),
	}
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewAgentGatewayServiceHandler(h.gw))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := agentv1connect.NewAgentGatewayServiceClient(srv.Client(), srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	opts := RunOptions{
		Policy: NewPolicy(localCap, nil),
		RestartContainer: func(_ context.Context, id string) error {
			h.restarts <- id
			return nil
		},
	}
	go func() { defer close(done); _ = session(ctx, client, discardLogger(), opts) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

func (h *sessionHarness) send(msg *agentv1.ConnectResponse) { h.gw.out <- msg }

func (h *sessionHarness) command(t *testing.T, id string, auth agentv1.Authorization) *agentv1.CommandResult {
	t.Helper()
	c := kindCommand("restart_container", auth)
	c.Id = id
	h.send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Command{Command: c}})
	select {
	case r := <-h.gw.results:
		if r.GetCommandId() != id {
			t.Fatalf("result id = %q, want %q", r.GetCommandId(), id)
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
		return nil
	}
}

func (h *sessionHarness) restarted() bool {
	select {
	case <-h.restarts:
		return true
	default:
		return false
	}
}

func policyMsg(level string) *agentv1.ConnectResponse {
	return &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Policy{Policy: &agentv1.Policy{Autonomy: level}}}
}

func TestSessionRefusesAboveLevel(t *testing.T) {
	const (
		P = agentv1.Authorization_AUTHORIZATION_APPROVED
		A = agentv1.Authorization_AUTHORIZATION_AUTO
		U = agentv1.Authorization_AUTHORIZATION_UNSPECIFIED
	)
	t.Run("auto at suggest", func(t *testing.T) {
		h := startSession(t, "suggest", "")
		r := h.command(t, "id-1", A)
		if !strings.HasPrefix(r.GetError(), "refused: autonomy level suggest") || h.restarted() {
			t.Fatalf("result=%v", r)
		}
	})
	t.Run("approved at report", func(t *testing.T) {
		h := startSession(t, "report", "")
		if r := h.command(t, "id-2", P); r.GetError() == "" || h.restarted() {
			t.Fatalf("result=%v", r)
		}
	})
	t.Run("unspecified at act", func(t *testing.T) {
		h := startSession(t, "act", "")
		r := h.command(t, "id-3", U)
		if r.GetError() != "refused: operator command needs an authorization" || h.restarted() {
			t.Fatalf("result=%v", r)
		}
	})
	t.Run("auto at act", func(t *testing.T) {
		h := startSession(t, "act", "")
		if r := h.command(t, "id-4", A); r.GetError() != "" {
			t.Fatalf("result=%v", r)
		}
		if !h.restarted() {
			t.Fatal("restart handler not reached")
		}
	})
	t.Run("local cap beats announcement", func(t *testing.T) {
		h := startSession(t, "act", "report")
		if r := h.command(t, "id-5", A); r.GetError() == "" || h.restarted() {
			t.Fatalf("result=%v", r)
		}
	})
	t.Run("policy message changes level live", func(t *testing.T) {
		h := startSession(t, "report", "")
		if r := h.command(t, "a", A); r.GetError() == "" {
			t.Fatal("auto allowed at report")
		}
		h.send(policyMsg("act"))
		if r := h.command(t, "b", A); r.GetError() != "" || !h.restarted() {
			t.Fatalf("after raise: %v", r)
		}
		h.send(policyMsg("report"))
		if r := h.command(t, "c", A); r.GetError() == "" || h.restarted() {
			t.Fatalf("after lower: %v", r)
		}
	})
}

func TestDispatchRestartWithoutHandlerIsUnsupported(t *testing.T) {
	p := NewPolicy("", nil)
	p.SetServerLevel("act")
	r := dispatch(context.Background(), kindCommand("restart_container", agentv1.Authorization_AUTHORIZATION_AUTO), RunOptions{Policy: p})
	if r.GetError() != "unsupported command" {
		t.Fatalf("got %q", r.GetError())
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
