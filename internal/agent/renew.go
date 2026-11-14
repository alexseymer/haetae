package agent

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/pki"
)

// Certificate renewal timing. The agent renews when fewer than
// DefaultRenewBefore remain, checking at each connect and then every
// DefaultRenewCheckEvery. A failed attempt is retried after a growing delay;
// failures only log, the session keeps running on its current certificate.
const (
	DefaultRenewBefore     = 30 * 24 * time.Hour
	DefaultRenewCheckEvery = 24 * time.Hour
	renewRetryMin          = time.Minute
	renewRetryMax          = 6 * time.Hour
	renewReplyTimeout      = 30 * time.Second
)

// renewer keeps the agent's current client certificate and renews it over the
// live session. It holds one key and certificate and a one-slot reply channel;
// nothing else is buffered.
type renewer struct {
	dir         string
	hostID      string
	caDER       []byte
	renewBefore time.Duration
	checkEvery  time.Duration
	log         *slog.Logger

	mu       sync.Mutex
	cert     tls.Certificate // what the next TLS handshake presents
	notAfter time.Time

	reply chan *agentv1.RenewCertificateResponse
}

func newRenewer(id *Identity, dir string, before, every time.Duration, log *slog.Logger) (*renewer, error) {
	r := &renewer{
		dir: dir, hostID: id.HostID, caDER: id.CADER,
		renewBefore: before, checkEvery: every, log: log,
		reply: make(chan *agentv1.RenewCertificateResponse, 1),
	}
	if r.renewBefore <= 0 {
		r.renewBefore = DefaultRenewBefore
	}
	if r.checkEvery <= 0 {
		r.checkEvery = DefaultRenewCheckEvery
	}
	if err := r.set(id.CertDER, id.Key); err != nil {
		return nil, err
	}
	return r, nil
}

// set makes certDER and key the certificate presented from now on.
func (r *renewer) set(certDER []byte, key crypto.Signer) error {
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("agent: parse client certificate: %w", err)
	}
	r.mu.Lock()
	r.cert = tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key, Leaf: leaf}
	r.notAfter = leaf.NotAfter
	r.mu.Unlock()
	return nil
}

// clientCertificate is the tls.Config.GetClientCertificate callback: every
// new connection picks up the latest certificate.
func (r *renewer) clientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cert
	return &c, nil
}

func (r *renewer) due(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notAfter.Sub(now) < r.renewBefore
}

// deliver hands the server's answer to the waiting renewal. A late or
// unsolicited answer is dropped.
func (r *renewer) deliver(resp *agentv1.RenewCertificateResponse) {
	select {
	case r.reply <- resp:
	default:
	}
}

// run checks for renewal at once and then every checkEvery until ctx ends
// (the session). A failed attempt is retried with exponential backoff, capped
// at renewRetryMax. It never ends the session.
func (r *renewer) run(ctx context.Context, send func(*agentv1.ConnectRequest) error) {
	var delay time.Duration // first check is immediate: "at connect"
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if !r.due(time.Now()) {
			delay, fails = r.checkEvery, 0
			continue
		}
		if err := r.renewOnce(ctx, send); err != nil {
			fails++
			delay = min(renewRetryMin<<min(fails-1, 20), renewRetryMax, r.checkEvery)
			r.log.Warn("certificate renewal failed, will retry", "err", err, "in", delay)
			continue
		}
		delay, fails = r.checkEvery, 0
	}
}

// renewOnce sends a CSR with a fresh key and, if the answer is good, saves
// the new certificate and key and starts using them for later connections.
// Order matters: the pair is checked, then written to disk, and only then
// used in memory, so a failure at any step leaves the old, working pair.
func (r *renewer) renewOnce(ctx context.Context, send func(*agentv1.ConnectRequest) error) error {
	key, csr, err := pki.NewKeyAndCSR()
	if err != nil {
		return err
	}
	select { // drop a stale answer from an earlier attempt
	case <-r.reply:
	default:
	}
	req := &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_RenewCertificate{RenewCertificate: &agentv1.RenewCertificateRequest{CsrDer: csr}}}
	if err := send(req); err != nil {
		return fmt.Errorf("send renewal request: %w", err)
	}

	var resp *agentv1.RenewCertificateResponse
	select {
	case resp = <-r.reply:
	case <-time.After(renewReplyTimeout):
		return errors.New("no answer to renewal request")
	case <-ctx.Done():
		return ctx.Err()
	}
	if e := resp.GetError(); e != "" {
		return fmt.Errorf("server refused renewal: %s", e)
	}
	der := resp.GetCertificateDer()
	if err := r.verify(der, key); err != nil {
		return err
	}
	if err := SaveRenewed(r.dir, r.hostID, der, key); err != nil {
		return fmt.Errorf("save renewed certificate: %w", err)
	}
	if err := r.set(der, key); err != nil {
		return err
	}
	r.mu.Lock()
	exp := r.notAfter
	r.mu.Unlock()
	r.log.Info("client certificate renewed", "expires", exp.UTC().Format(time.RFC3339))
	return nil
}

// verify accepts the new certificate only if it chains to the pinned CA, is
// valid now, is for this host, and belongs to the key just generated.
func (r *renewer) verify(der []byte, key crypto.Signer) error {
	if err := checkCertKey(der, key, r.hostID); err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(r.caDER)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("renewed certificate does not verify: %w", err)
	}
	r.mu.Lock()
	cur := r.notAfter
	r.mu.Unlock()
	if !cert.NotAfter.After(cur) {
		return errors.New("renewed certificate does not extend the lifetime")
	}
	return nil
}
