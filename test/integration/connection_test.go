//go:build integration

// Package integration holds end-to-end tests that run the gateway, a real
// PostgreSQL and the agent together. Run with: make test-integration
package integration

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/gateway"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/gen/haetae/agent/v1/agentv1connect"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/server"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

// env is one control plane: database, CA and a gateway on a fixed address.
type env struct {
	t     *testing.T
	st    *store.Store
	ca    *pki.CA
	org   store.Org
	addr  string
	gw    *gateway.Gateway
	gs    *server.GatewayServer
	log   *slog.Logger
	gwCfg gateway.Config
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	org, err := st.DefaultOrg(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{
		t:     t,
		st:    st,
		ca:    ca,
		org:   org,
		addr:  freeAddr(t),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		gwCfg: gateway.Config{Heartbeat: time.Second},
	}
	if testing.Verbose() {
		e.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	e.startGateway()
	t.Cleanup(func() { e.stopGateway() })
	return e
}

func (e *env) startGateway() {
	e.t.Helper()
	e.gw = gateway.New(e.st, e.ca, e.gwCfg, e.log)
	gs, err := server.ListenGateway(e.addr, e.gw, e.ca, []string{"127.0.0.1", "localhost"})
	if err != nil {
		e.t.Fatal(err)
	}
	e.gs = gs
}

func (e *env) stopGateway() {
	if e.gs != nil {
		_ = e.gs.Close()
		e.gs = nil
	}
}

func (e *env) token(name string) string {
	e.t.Helper()
	tok, _, err := e.st.CreateEnrollmentToken(context.Background(), e.org.ID, name, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *env) pin() string { return pki.Fingerprint(e.ca.DER) }

func (e *env) host(id string) store.Host {
	e.t.Helper()
	h, err := e.st.GetHost(context.Background(), e.org.ID, uuid.MustParse(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func (e *env) waitConnected(id string, want bool, timeout time.Duration) time.Duration {
	e.t.Helper()
	start := time.Now()
	waitFor(e.t, timeout, fmt.Sprintf("host %s connected=%v", id, want), func() bool {
		return e.host(id).Connected == want
	})
	return time.Since(start)
}

// runAgent runs the agent in-process until the returned stop function is called.
func (e *env) runAgent(id *agent.Identity) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = agent.Run(ctx, id, e.log.With("component", "agent"))
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	e.t.Cleanup(stop)
	return stop
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// S1: online within 30 s of enrollment; token is single-use; commands round-trip.
func TestEnrollConnectAndSingleUseToken(t *testing.T) {
	e := newEnv(t)
	tok := e.token("hel1-prod")
	url := "https://" + e.addr

	start := time.Now()
	id, err := agent.Enroll(context.Background(), url, tok, e.pin(), t.TempDir())
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	e.runAgent(id)
	e.waitConnected(id.HostID, true, 30*time.Second)
	online := time.Since(start)
	t.Logf("MEASURE S1 enrollment-to-online: %s (limit 30s)", online.Round(time.Millisecond))
	if online > 30*time.Second {
		t.Fatalf("online after %s, want < 30s", online)
	}

	if got := e.host(id.HostID).Name; got != "hel1-prod" {
		t.Errorf("host name = %q, want hel1-prod", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.gw.Send(ctx, uuid.MustParse(id.HostID), &agentv1.Command{Kind: &agentv1.Command_Ping{Ping: &agentv1.Ping{}}})
	if err != nil || res.GetError() != "" {
		t.Fatalf("ping: res=%v err=%v", res, err)
	}

	_, err = agent.Enroll(context.Background(), url, tok, e.pin(), t.TempDir())
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("second use of token: err=%v, want permission_denied", err)
	}
}

func TestEnrollRejectsWrongPin(t *testing.T) {
	e := newEnv(t)
	other, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Enroll(context.Background(), "https://"+e.addr, e.token("x"), pki.Fingerprint(other.DER), t.TempDir())
	if err == nil {
		t.Fatal("enroll with a wrong CA pin succeeded")
	}
}

func TestConnectRequiresClientCertificate(t *testing.T) {
	e := newEnv(t)
	client := agentv1connect.NewAgentGatewayServiceClient(
		httpClientNoCert(e.pin()), "https://"+e.addr)
	stream := client.Connect(context.Background())
	_ = stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{}}})
	_, err := stream.Receive()
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("connect without client cert: err=%v, want unauthenticated", err)
	}
}

// S10: the agent reconnects by itself after the server restarts.
func TestReconnectAfterServerRestart(t *testing.T) {
	e := newEnv(t)
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("a"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.runAgent(id)
	e.waitConnected(id.HostID, true, 30*time.Second)

	e.stopGateway()
	e.waitConnected(id.HostID, false, 10*time.Second)
	time.Sleep(2 * time.Second) // let the agent hit at least one failed reconnect
	e.startGateway()

	took := e.waitConnected(id.HostID, true, 45*time.Second)
	t.Logf("MEASURE S10 reconnect after server restart: %s", took.Round(time.Millisecond))
}

// S10: after an agent restart (reboot) it reuses its stored identity.
func TestReconnectAfterAgentRestart(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("b"), e.pin(), dir)
	if err != nil {
		t.Fatal(err)
	}
	stop := e.runAgent(id)
	e.waitConnected(id.HostID, true, 30*time.Second)

	stop()
	e.waitConnected(id.HostID, false, 10*time.Second)

	reloaded, err := agent.LoadIdentity(dir)
	if err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if reloaded.HostID != id.HostID {
		t.Fatalf("host id changed after restart: %s != %s", reloaded.HostID, id.HostID)
	}
	e.runAgent(reloaded)
	took := e.waitConnected(id.HostID, true, 30*time.Second)
	t.Logf("MEASURE S10 reconnect after agent restart: %s", took.Round(time.Millisecond))

	hosts, err := e.st.ListHosts(context.Background(), e.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts after restart, want 1 (no re-enrollment)", len(hosts))
	}
}

// S10: the agent reconnects after the network is cut and restored.
func TestReconnectAfterNetworkCut(t *testing.T) {
	e := newEnv(t)
	px := newProxy(t, e.addr)
	id, err := agent.Enroll(context.Background(), "https://"+px.addr, e.token("c"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.runAgent(id)
	e.waitConnected(id.HostID, true, 30*time.Second)

	px.cut()
	e.waitConnected(id.HostID, false, 20*time.Second)
	time.Sleep(3 * time.Second)
	px.restore()

	took := e.waitConnected(id.HostID, true, 45*time.Second)
	t.Logf("MEASURE S10 reconnect after network restore: %s", took.Round(time.Millisecond))
}

// S10: the agent also recovers when the path silently drops packets (no
// reset), which only HTTP/2 pings and heartbeats can detect.
func TestReconnectAfterSilentNetworkLoss(t *testing.T) {
	e := newEnv(t)
	px := newProxy(t, e.addr)
	id, err := agent.Enroll(context.Background(), "https://"+px.addr, e.token("e"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.runAgent(id)
	e.waitConnected(id.HostID, true, 30*time.Second)

	before := px.agentSideConns()
	if len(before) != 1 {
		t.Fatalf("expected one agent connection before the outage, got %d", len(before))
	}
	px.blackhole(true)
	start := time.Now()
	e.waitConnected(id.HostID, false, 20*time.Second) // server: 3 missed heartbeats

	// The agent itself must notice the dead path (HTTP/2 ping timeout) and
	// close its connection while traffic is still being dropped; otherwise it
	// would only recover thanks to a reset after the network returns.
	waitFor(t, 40*time.Second, "agent closes its dead connection during the outage", func() bool {
		return px.closedByAgent(before[0])
	})
	t.Logf("MEASURE S10 agent detected silent loss after: %s", time.Since(start).Round(time.Millisecond))
	px.blackhole(false)

	took := e.waitConnected(id.HostID, true, 60*time.Second)
	t.Logf("MEASURE S10 reconnect after silent loss ends: %s", took.Round(time.Millisecond))
}

func TestEnrollRejectsDuplicateHostName(t *testing.T) {
	e := newEnv(t)
	first, second := e.token("same-name"), e.token("same-name")
	if _, err := agent.Enroll(context.Background(), "https://"+e.addr, first, e.pin(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_, err := agent.Enroll(context.Background(), "https://"+e.addr, second, e.pin(), t.TempDir())
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate host name: err=%v, want already_exists", err)
	}
}

// A certificate stops working once the host's stored serial changes
// (re-issued) or the host is revoked.
func TestSupersededOrRevokedCertificateIsRejected(t *testing.T) {
	e := newEnv(t)
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("f"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connectOnce := func() error {
		cfg, err := pki.ClientTLSConfig(id.CADER, id.CertDER, id.Key, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		client := agentv1connect.NewAgentGatewayServiceClient(httpClientTLS(cfg), "https://"+e.addr)
		stream := client.Connect(ctx)
		defer stream.CloseRequest()
		_ = stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{}}})
		_, err = stream.Receive()
		return err
	}
	if err := connectOnce(); err != nil {
		t.Fatalf("valid certificate rejected: %v", err)
	}

	if _, err := e.st.Pool().Exec(ctx, `UPDATE hosts SET cert_serial = 'ffff' WHERE id = $1`, id.HostID); err != nil {
		t.Fatal(err)
	}
	if err := connectOnce(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("superseded certificate: err=%v, want permission_denied", err)
	}

	if _, err := e.st.Pool().Exec(ctx, `UPDATE hosts SET cert_serial = $2, revoked_at = now() WHERE id = $1`, id.HostID, e.host(id.HostID).CertSerial); err != nil {
		t.Fatal(err)
	}
	if err := connectOnce(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("revoked host: err=%v, want permission_denied", err)
	}
}

// S1: the agent process opens no listening socket.
func TestAgentOpensNoListeningSocket(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("d"), e.pin(), dir)
	if err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(t.TempDir(), "haetae-agent")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/haetae-agent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "run", "--state-dir", dir)
	if testing.Verbose() {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	e.waitConnected(id.HostID, true, 30*time.Second)
	listening, total := listeningSockets(t, cmd.Process.Pid)
	t.Logf("agent pid %d has %d sockets, %d listening", cmd.Process.Pid, total, len(listening))
	if total == 0 {
		t.Fatal("found no sockets at all; the inspection is broken")
	}
	if len(listening) > 0 {
		t.Fatalf("agent has listening sockets: %v", listening)
	}
}

// listeningSockets returns the TCP sockets in LISTEN state and all UDP
// sockets owned by pid, plus the number of sockets the process holds.
func listeningSockets(t *testing.T, pid int) (listening []string, total int) {
	t.Helper()
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		t.Fatal(err)
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err != nil {
			continue
		}
		if ino, ok := strings.CutPrefix(link, "socket:["); ok {
			inodes[strings.TrimSuffix(ino, "]")] = true
		}
	}
	total = len(inodes)

	for _, table := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f, err := os.Open(fmt.Sprintf("/proc/%d/net/%s", pid, table))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || !inodes[fields[9]] {
				continue
			}
			isUDP := strings.HasPrefix(table, "udp")
			if isUDP || fields[3] == "0A" {
				listening = append(listening, table+" "+fields[1])
			}
		}
		f.Close()
	}
	return listening, total
}
