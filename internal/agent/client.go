package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/gen/haetae/agent/v1/agentv1connect"
	"github.com/alexseymer/haetae/internal/pki"
)

// ErrAlreadyEnrolled is returned by Enroll when dir already holds an identity.
var ErrAlreadyEnrolled = errors.New("agent: already enrolled")

// Backoff limits for reconnecting. A session that lasted longer than
// stableSession resets the backoff.
const (
	backoffMin    = time.Second
	backoffMax    = 30 * time.Second
	stableSession = time.Minute
)

// httpClient builds an HTTP/2 client. HTTP/2 pings detect a dead connection
// (for example after a network cut without a reset) so the session fails
// fast and the agent reconnects.
func httpClient(cfg *tls.Config) *http.Client {
	tr := &http.Transport{
		TLSClientConfig:     cfg,
		ForceAttemptHTTP2:   true,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 15 * time.Second,
			PingTimeout:     10 * time.Second,
		},
	}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	return &http.Client{Transport: tr}
}

func serverName(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("agent: server URL must look like https://host:port, got %q", serverURL)
	}
	return u.Hostname(), nil
}

// Enroll trades a single-use token for a client certificate and saves the
// resulting identity in dir. caPin is the "sha256:..." fingerprint of the
// control plane's CA, printed with the enrollment command.
func Enroll(ctx context.Context, serverURL, token, caPin, dir string) (*Identity, error) {
	if _, err := LoadIdentity(dir); err == nil {
		return nil, ErrAlreadyEnrolled
	} else if !errors.Is(err, ErrNotEnrolled) {
		return nil, err
	}
	name, err := serverName(serverURL)
	if err != nil {
		return nil, err
	}

	key, csr, err := pki.NewKeyAndCSR()
	if err != nil {
		return nil, err
	}
	client := agentv1connect.NewAgentGatewayServiceClient(httpClient(pki.PinnedTLSConfig(caPin, name)), serverURL)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.Enroll(ctx, connect.NewRequest(&agentv1.EnrollRequest{
		Token:  token,
		CsrDer: csr,
		Host:   collectHostInfo(),
	}))
	if err != nil {
		return nil, fmt.Errorf("agent: enroll: %w", err)
	}

	id := &Identity{
		HostID:     resp.Msg.GetHostId(),
		ServerURL:  serverURL,
		ServerName: name,
		CertDER:    resp.Msg.GetCertificateDer(),
		CADER:      resp.Msg.GetCaCertificateDer(),
		Key:        key,
	}
	// The CA we were given must be the one we pinned.
	if pki.Fingerprint(id.CADER) != caPin {
		return nil, errors.New("agent: enroll: server returned a CA that does not match the pin")
	}
	if err := id.Save(dir); err != nil {
		return nil, err
	}
	return id, nil
}

// Run keeps a session to the control plane until ctx is cancelled,
// reconnecting with jittered exponential backoff.
func Run(ctx context.Context, id *Identity, log *slog.Logger) error {
	tlsCfg, err := pki.ClientTLSConfig(id.CADER, id.CertDER, id.Key, id.ServerName)
	if err != nil {
		return err
	}
	client := agentv1connect.NewAgentGatewayServiceClient(httpClient(tlsCfg), id.ServerURL)

	backoff := backoffMin
	for {
		started := time.Now()
		err := session(ctx, client, log)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > stableSession {
			backoff = backoffMin
		}
		// Full jitter: wait a random time in [backoff/2, backoff].
		wait := backoff/2 + rand.N(backoff/2+1)
		log.Warn("session ended, reconnecting", "err", err, "in", wait.Round(time.Millisecond))

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

func session(ctx context.Context, client agentv1connect.AgentGatewayServiceClient, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream := client.Connect(ctx)
	defer stream.CloseRequest() //nolint:errcheck // best effort on shutdown

	// connect-go allows Send concurrently with Receive, but not with itself.
	var sendMu sync.Mutex
	send := func(msg *agentv1.ConnectRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	if err := send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{Host: collectHostInfo()}}}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	first, err := stream.Receive()
	if err != nil {
		return fmt.Errorf("receive welcome: %w", err)
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		return errors.New("first server message was not welcome")
	}
	interval := time.Duration(welcome.GetHeartbeatSeconds()) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}
	log.Info("connected", "heartbeat", interval)

	hbErr := make(chan error, 1)
	go func() { hbErr <- heartbeat(ctx, interval, send) }()

	recvErr := make(chan error, 1)
	go func() { recvErr <- receiveCommands(stream, send) }()

	select {
	case err := <-hbErr:
		return err
	case err := <-recvErr:
		return err
	case <-ctx.Done():
		return nil
	}
}

func heartbeat(ctx context.Context, interval time.Duration, send func(*agentv1.ConnectRequest) error) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			msg := &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{SentUnixMs: time.Now().UnixMilli()}}}
			if err := send(msg); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
		}
	}
}

func receiveCommands(stream *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse], send func(*agentv1.ConnectRequest) error) error {
	for {
		msg, err := stream.Receive()
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}
		cmd := msg.GetCommand()
		if cmd == nil {
			continue
		}
		res := execute(cmd)
		if err := send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Result{Result: res}}); err != nil {
			return fmt.Errorf("send result: %w", err)
		}
	}
}

// execute runs one command. Only kinds listed here exist; anything else is
// refused. This switch is the agent's command allowlist.
func execute(cmd *agentv1.Command) *agentv1.CommandResult {
	res := &agentv1.CommandResult{CommandId: cmd.GetId()}
	switch cmd.GetKind().(type) {
	case *agentv1.Command_Ping:
		// Nothing to do: answering proves the session works end to end.
	default:
		res.Error = "unsupported command"
	}
	return res
}
