// Package gateway is the control plane's side of the agent connection: it
// enrolls hosts and keeps one session per connected agent.
package gateway

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/gen/haetae/agent/v1/agentv1connect"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/store"
)

// MaxMessageBytes is the largest agent message the gateway accepts. The
// biggest legitimate message (5000 container samples) is about 0.5 MB.
const MaxMessageBytes = 4 << 20

// ErrNotConnected is returned by Send when the host has no live session.
var ErrNotConnected = errors.New("gateway: host not connected")

// Store is the persistence the gateway needs. *store.Store implements it.
type Store interface {
	EnrollHost(ctx context.Context, token string, info store.HostInfo, issue func(hostID uuid.UUID) (string, error)) (store.Host, error)
	HostForAgent(ctx context.Context, id uuid.UUID) (store.Host, error)
	MarkConnected(ctx context.Context, id uuid.UUID, info store.HostInfo, at time.Time) error
	MarkSeen(ctx context.Context, id uuid.UUID, at time.Time) error
	MarkDisconnected(ctx context.Context, id uuid.UUID) error
	ingestStore
	logStore
}

// Config tunes the gateway. Zero values get defaults.
type Config struct {
	// Heartbeat is the interval agents are told to use. A session with no
	// message for three intervals is closed. Default 10 s.
	Heartbeat time.Duration
	// ClientCertTTL is the lifetime of issued client certificates. Default one year.
	ClientCertTTL time.Duration
	// HelloTimeout bounds the wait for the agent's first message. Default 10 s.
	HelloTimeout time.Duration
	// SeenWriteEvery limits how often last_seen_at is written per host.
	// Default 30 s.
	SeenWriteEvery time.Duration
	// JudgeEvery limits how often an org's new log templates are judged.
	// Default 10 s.
	JudgeEvery time.Duration
	// Tap, if set, sees every message received from an agent. For tests.
	Tap func(hostID uuid.UUID, msg *agentv1.ConnectRequest)
}

func (c Config) withDefaults() Config {
	if c.Heartbeat <= 0 {
		c.Heartbeat = 10 * time.Second
	}
	// Sent to agents in whole seconds, so keep it between 1 s and 1 h.
	c.Heartbeat = min(max(c.Heartbeat, time.Second), time.Hour)
	if c.ClientCertTTL <= 0 {
		c.ClientCertTTL = 365 * 24 * time.Hour
	}
	if c.HelloTimeout <= 0 {
		c.HelloTimeout = 10 * time.Second
	}
	if c.SeenWriteEvery <= 0 {
		c.SeenWriteEvery = 30 * time.Second
	}
	if c.JudgeEvery <= 0 {
		c.JudgeEvery = 10 * time.Second
	}
	return c
}

// Gateway implements agentv1connect.AgentGatewayServiceHandler.
type Gateway struct {
	store Store
	ca    *pki.CA
	cfg   Config
	log   *slog.Logger

	mu       sync.Mutex
	sessions map[uuid.UUID]*session

	judgeMu sync.Mutex
	judge   judgeThrottle
}

var _ agentv1connect.AgentGatewayServiceHandler = (*Gateway)(nil)

// New returns a gateway that issues client certificates from ca.
func New(st Store, ca *pki.CA, cfg Config, log *slog.Logger) *Gateway {
	return &Gateway{
		store:    st,
		ca:       ca,
		cfg:      cfg.withDefaults(),
		log:      log,
		sessions: make(map[uuid.UUID]*session),
		judge:    judgeThrottle{every: cfg.withDefaults().JudgeEvery},
	}
}

// Handler returns the HTTP handler to mount at the returned path. It must be
// served over TLS configured with the CA's ServerTLSConfig, so verified client
// certificates reach Connect.
func (g *Gateway) Handler() (string, http.Handler) {
	path, h := agentv1connect.NewAgentGatewayServiceHandler(g,
		// Bound every message before it is decoded; the caps in ingest.go
		// only apply afterwards.
		connect.WithReadMaxBytes(MaxMessageBytes),
	)
	return path, peerIdentity(h)
}

// maxInfoLen caps host-reported strings; the agent is not trusted to be sane.
const maxInfoLen = 256

// clip cuts agent-supplied strings to maxInfoLen bytes on a rune boundary
// (PostgreSQL rejects invalid UTF-8).
func clip(s string) string {
	if len(s) <= maxInfoLen {
		return s
	}
	s = s[:maxInfoLen]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func hostInfo(h *agentv1.HostInfo) store.HostInfo {
	return store.HostInfo{
		Hostname:      clip(h.GetHostname()),
		OS:            clip(h.GetOs()),
		Kernel:        clip(h.GetKernel()),
		Arch:          clip(h.GetArch()),
		DockerVersion: clip(h.GetDockerVersion()),
		AgentVersion:  clip(h.GetAgentVersion()),
	}
}

// Enroll trades a single-use token and a CSR for a client certificate.
func (g *Gateway) Enroll(ctx context.Context, req *connect.Request[agentv1.EnrollRequest]) (*connect.Response[agentv1.EnrollResponse], error) {
	msg := req.Msg
	if msg.GetToken() == "" || len(msg.GetCsrDer()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("token and csr are required"))
	}
	if csr, err := x509.ParseCertificateRequest(msg.GetCsrDer()); err != nil || csr.CheckSignature() != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate signing request is malformed"))
	}

	var certDER []byte
	host, err := g.store.EnrollHost(ctx, msg.GetToken(), hostInfo(msg.GetHost()), func(hostID uuid.UUID) (string, error) {
		der, serial, err := g.ca.SignClientCSR(msg.GetCsrDer(), hostID.String(), g.cfg.ClientCertTTL)
		if err != nil {
			return "", err
		}
		certDER = der
		return serial, nil
	})
	switch {
	case errors.Is(err, store.ErrTokenInvalid):
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("enrollment token is invalid, expired or already used"))
	case errors.Is(err, store.ErrHostNameTaken):
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a host with this name is already enrolled"))
	case err != nil:
		g.log.Error("enroll failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("enrollment failed"))
	}

	g.log.Info("host enrolled", "host_id", host.ID, "name", host.Name)
	return connect.NewResponse(&agentv1.EnrollResponse{
		HostId:           host.ID.String(),
		CertificateDer:   certDER,
		CaCertificateDer: g.ca.DER,
	}), nil
}

// Connect runs one agent session until the agent leaves, goes quiet, or a
// newer session for the same host replaces it.
func (g *Gateway) Connect(ctx context.Context, stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]) error {
	host, err := g.authenticate(ctx)
	if err != nil {
		return err
	}
	hostID := host.ID

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	hello, err := receiveHello(ctx, stream, g.cfg.HelloTimeout)
	if err != nil {
		return err
	}
	if g.cfg.Tap != nil {
		g.cfg.Tap(host.ID, &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: hello}})
	}

	s := newSession(hostID, cancel)
	s.orgID = host.OrgID
	g.register(s)
	defer g.unregister(s)

	if err := g.store.MarkConnected(ctx, hostID, hostInfo(hello.GetHost()), time.Now()); err != nil {
		g.log.Error("mark connected", "host_id", hostID, "err", err)
		return connect.NewError(connect.CodeUnavailable, errors.New("server busy, retry"))
	}
	g.log.Info("agent connected", "host_id", hostID)

	welcome := &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Welcome{Welcome: &agentv1.Welcome{
		HeartbeatSeconds: uint32(g.cfg.Heartbeat / time.Second), //nolint:gosec // G115: capped at one hour in withDefaults
	}}}
	if err := stream.Send(welcome); err != nil {
		return err
	}
	// Tell the agent which templates are already green for this org, so it
	// skips them from its first line.
	if ids, err := g.store.GreenTemplateIDs(ctx, host.OrgID, maxGreenIDs); err != nil {
		g.log.Warn("load green set", "host_id", hostID, "err", err)
	} else if len(ids) > 0 {
		s.setGreen(ids)
	}

	recvErr := make(chan error, 1)
	go func() { recvErr <- g.receiveLoop(ctx, stream, s) }()

	err = g.sendLoop(ctx, stream, s, recvErr)
	g.log.Info("agent disconnected", "host_id", hostID, "reason", err)
	return nil
}

func (g *Gateway) authenticate(ctx context.Context) (store.Host, error) {
	cert, ok := ctx.Value(peerCertKey{}).(*x509.Certificate)
	if !ok {
		return store.Host{}, connect.NewError(connect.CodeUnauthenticated, errors.New("client certificate required"))
	}
	raw, err := pki.HostIDFromCert(cert)
	if err != nil {
		return store.Host{}, connect.NewError(connect.CodeUnauthenticated, errors.New("client certificate has no host identity"))
	}
	hostID, err := uuid.Parse(raw)
	if err != nil {
		return store.Host{}, connect.NewError(connect.CodeUnauthenticated, errors.New("client certificate has an invalid host id"))
	}

	host, err := g.store.HostForAgent(ctx, hostID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Host{}, connect.NewError(connect.CodePermissionDenied, errors.New("unknown host"))
	}
	if err != nil {
		g.log.Error("get host", "host_id", hostID, "err", err)
		return store.Host{}, connect.NewError(connect.CodeUnavailable, errors.New("server busy, retry"))
	}
	if host.RevokedAt != nil {
		return store.Host{}, connect.NewError(connect.CodePermissionDenied, errors.New("host revoked"))
	}
	// Only the most recently issued certificate is valid for a host.
	if normalizeSerial(host.CertSerial) != normalizeSerial(cert.SerialNumber.Text(16)) {
		return store.Host{}, connect.NewError(connect.CodePermissionDenied, errors.New("certificate superseded"))
	}
	return host, nil
}

func normalizeSerial(s string) string {
	return strings.TrimLeft(strings.ToLower(s), "0")
}

func receiveHello(ctx context.Context, stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse], timeout time.Duration) (*agentv1.Hello, error) {
	type result struct {
		msg *agentv1.ConnectRequest
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := stream.Receive()
		ch <- result{msg, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		hello := r.msg.GetHello()
		if hello == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("first message must be hello"))
		}
		return hello, nil
	case <-time.After(timeout):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("no hello received"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *Gateway) receiveLoop(ctx context.Context, stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse], s *session) error {
	lastSeenWrite := time.Now() // MarkConnected just wrote it
	for {
		msg, err := stream.Receive()
		if err != nil {
			return err
		}
		s.touch()
		if g.cfg.Tap != nil {
			g.cfg.Tap(s.hostID, msg)
		}
		switch m := msg.GetMsg().(type) {
		case *agentv1.ConnectRequest_Heartbeat:
			if time.Since(lastSeenWrite) >= g.cfg.SeenWriteEvery {
				if err := g.store.MarkSeen(ctx, s.hostID, time.Now()); err != nil {
					g.log.Warn("mark seen", "host_id", s.hostID, "err", err)
				}
				lastSeenWrite = time.Now()
			}
		case *agentv1.ConnectRequest_State:
			g.ingestState(ctx, s, m.State)
		case *agentv1.ConnectRequest_Metrics:
			g.ingestMetrics(ctx, s, m.Metrics)
		case *agentv1.ConnectRequest_Logs:
			g.ingestLogs(ctx, s, m.Logs)
		case *agentv1.ConnectRequest_Result:
			s.deliver(m.Result)
		case *agentv1.ConnectRequest_Hello:
			return connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate hello"))
		default:
			// Unknown message kinds come from newer agents; ignore them.
		}
	}
}

func (g *Gateway) sendLoop(ctx context.Context, stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse], s *session, recvErr <-chan error) error {
	idleLimit := 3 * g.cfg.Heartbeat
	check := time.NewTicker(g.cfg.Heartbeat)
	defer check.Stop()

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case err := <-recvErr:
			return err
		case <-check.C:
			if s.idle() > idleLimit {
				return fmt.Errorf("no message for %s", idleLimit)
			}
		case ids := <-s.green:
			msg := &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Green{Green: &agentv1.GreenSet{TemplateIds: ids}}}
			if err := stream.Send(msg); err != nil {
				return err
			}
		case cmd := <-s.outbox:
			if err := stream.Send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Command{Command: cmd}}); err != nil {
				return err
			}
		}
	}
}

func (g *Gateway) register(s *session) {
	g.mu.Lock()
	old := g.sessions[s.hostID]
	g.sessions[s.hostID] = s
	g.mu.Unlock()
	if old != nil {
		old.cancel(errors.New("replaced by a newer session"))
	}
}

func (g *Gateway) unregister(s *session) {
	g.mu.Lock()
	current := g.sessions[s.hostID] == s
	if current {
		delete(g.sessions, s.hostID)
	}
	g.mu.Unlock()
	s.close()

	// A replaced session must not mark the host offline under its successor.
	if !current {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.store.MarkDisconnected(ctx, s.hostID); err != nil {
		g.log.Error("mark disconnected", "host_id", s.hostID, "err", err)
	}
}

// pushGreen sends the org's green set to all its connected agents.
func (g *Gateway) pushGreen(ctx context.Context, orgID uuid.UUID) {
	ids, err := g.store.GreenTemplateIDs(ctx, orgID, maxGreenIDs)
	if err != nil {
		g.log.Warn("load green set", "org_id", orgID, "err", err)
		return
	}
	g.mu.Lock()
	var targets []*session
	for _, s := range g.sessions {
		if s.orgID == orgID {
			targets = append(targets, s)
		}
	}
	g.mu.Unlock()
	for _, s := range targets {
		s.setGreen(ids)
	}
}

// Connected reports whether the host has a live session on this gateway.
func (g *Gateway) Connected(hostID uuid.UUID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.sessions[hostID]
	return ok
}

// Send delivers a command to a connected host and waits for its result.
func (g *Gateway) Send(ctx context.Context, hostID uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error) {
	g.mu.Lock()
	s := g.sessions[hostID]
	g.mu.Unlock()
	if s == nil {
		return nil, ErrNotConnected
	}
	return s.call(ctx, cmd)
}

type peerCertKey struct{}

// peerIdentity passes the verified client certificate, if any, to handlers.
func peerIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
			r = r.WithContext(context.WithValue(r.Context(), peerCertKey{}, r.TLS.VerifiedChains[0][0]))
		}
		next.ServeHTTP(w, r)
	})
}
