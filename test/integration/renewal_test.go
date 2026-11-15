//go:build integration

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/gateway"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/gen/haetae/agent/v1/agentv1connect"
	"github.com/alexseymer/haetae/internal/pki"
)

func certOf(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// rawStream opens a Connect stream as the holder of id's certificate and
// completes the Hello/Welcome exchange. It uses the identity's exact
// certificate, not the agent's, so tests can play a misbehaving client.
func rawStream(t *testing.T, e *env, id *agent.Identity) *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse] {
	t.Helper()
	cfg, err := pki.ClientTLSConfig(id.CADER, id.CertDER, id.Key, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	t.Cleanup(tr.CloseIdleConnections)
	client := agentv1connect.NewAgentGatewayServiceClient(&http.Client{Transport: tr}, "https://"+e.addr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := client.Connect(ctx)
	if err := st.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{Host: &agentv1.HostInfo{Hostname: "raw"}}}}); err != nil {
		t.Fatal(err)
	}
	msg, err := st.Receive()
	if err != nil || msg.GetWelcome() == nil {
		t.Fatalf("no welcome: msg=%v err=%v", msg, err)
	}
	return st
}

func renewOn(t *testing.T, st *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse], csr []byte) *agentv1.RenewCertificateResponse {
	t.Helper()
	if err := st.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_RenewCertificate{RenewCertificate: &agentv1.RenewCertificateRequest{CsrDer: csr}}}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := st.Receive()
		if err != nil {
			t.Fatalf("receive renewal answer: %v", err)
		}
		if r := msg.GetRenewCertificate(); r != nil {
			return r
		}
	}
}

func csrWith(t *testing.T, uris ...string) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.CertificateRequest{}
	for _, u := range uris {
		pu, _ := url.Parse(u)
		tmpl.URIs = append(tmpl.URIs, pu)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, der
}

// The agent renews by itself before its certificate expires, persists the new
// one, and keeps working with it after a reconnect and after a restart, when
// the certificate it enrolled with has long expired.
func TestAgentRenewsCertificateBeforeExpiry(t *testing.T) {
	const ttl = 20 * time.Second
	e := newEnvWith(t, func(c *gateway.Config) { c.ClientCertTTL = ttl })
	dir := t.TempDir()
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("renew-me"), e.pin(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first := certOf(t, id.CertDER)
	hostID := uuid.MustParse(id.HostID)
	if h := e.host(id.HostID); h.CertExpiresAt == nil || h.CertExpiresAt.Sub(first.NotAfter).Abs() > time.Second {
		t.Fatalf("enrollment did not record cert expiry: %v vs %v", h.CertExpiresAt, first.NotAfter)
	}

	// Renew when 12 s are left, so the first renewal comes ~8 s after issue.
	opts := agent.RunOptions{StateDir: dir, RenewBefore: 12 * time.Second, RenewCheckEvery: 250 * time.Millisecond}
	stop := e.runAgentWith(id, opts, nil)
	e.waitConnected(id.HostID, true, 30*time.Second)
	firstSerial := e.host(id.HostID).CertSerial

	waitFor(t, 30*time.Second, "server records a renewed certificate", func() bool {
		return e.host(id.HostID).CertSerial != firstSerial
	})
	renewedAt := time.Now()
	if !renewedAt.Before(first.NotAfter) {
		t.Fatalf("renewed at %s, after the first certificate expired at %s", renewedAt, first.NotAfter)
	}
	h := e.host(id.HostID)
	if h.CertExpiresAt == nil || !h.CertExpiresAt.After(first.NotAfter) {
		t.Fatalf("cert_expires_at %v did not move past %v", h.CertExpiresAt, first.NotAfter)
	}
	// The session was never dropped by the renewal.
	if !h.Connected {
		t.Fatal("host dropped during renewal")
	}

	// Wait for the enrolled certificate to expire, then force a reconnect: only
	// the renewed one can get through TLS now.
	time.Sleep(time.Until(first.NotAfter) + time.Second)
	if !e.gw.DisconnectHost(hostID) {
		t.Fatal("no live session to disconnect")
	}
	e.waitConnected(id.HostID, false, 10*time.Second)
	e.waitConnected(id.HostID, true, 30*time.Second)
	t.Log("MEASURE reconnected on a renewed certificate after the enrolled one expired")

	// A restart reads the renewed pair from disk.
	stop()
	e.waitConnected(id.HostID, false, 10*time.Second)
	reloaded, err := agent.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !certOf(t, reloaded.CertDER).NotAfter.After(first.NotAfter) {
		t.Fatal("persisted identity still holds the enrolled certificate")
	}
	e.runAgentWith(reloaded, agent.RunOptions{}, nil)
	e.waitConnected(id.HostID, true, 30*time.Second)

	// The enrolled certificate, expired, is refused at the TLS layer.
	cfg, _ := pki.ClientTLSConfig(id.CADER, id.CertDER, id.Key, "127.0.0.1")
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	defer tr.CloseIdleConnections()
	c := agentv1connect.NewAgentGatewayServiceClient(&http.Client{Transport: tr}, "https://"+e.addr)
	st := c.Connect(context.Background())
	_ = st.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{}}})
	if _, err := st.Receive(); err == nil {
		t.Fatal("an expired certificate was accepted")
	}
}

// A stream holding host A's certificate cannot obtain a certificate for host
// B, and a renewal never changes identity.
func TestRenewalCannotChangeIdentity(t *testing.T) {
	e := newEnv(t)
	idA, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("host-a"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idB, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("host-b"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serialBefore := e.host(idB.HostID).CertSerial
	aBefore := e.host(idA.HostID).CertSerial
	st := rawStream(t, e, idA)

	// A CSR that names host B (URI SAN) is refused outright.
	_, csr := csrWith(t, "haetae://host/"+idB.HostID)
	if r := renewOn(t, st, csr); r.GetError() == "" || len(r.GetCertificateDer()) != 0 {
		t.Fatalf("CSR naming host B was answered with %+v", r)
	}
	// So is one with a CommonName of B's id.
	keyCN, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cnDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkixName(idB.HostID)}, keyCN)
	if r := renewOn(t, st, cnDER); r.GetError() == "" || len(r.GetCertificateDer()) != 0 {
		t.Fatalf("CSR with B's CommonName was answered with %+v", r)
	}
	// Reusing A's own key is refused too: renewal needs a fresh key.
	same, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, idA.Key)
	if r := renewOn(t, st, same); r.GetError() == "" {
		t.Fatalf("renewal with the old key was accepted: %+v", r)
	}
	// Neither host's certificate changed.
	if e.host(idB.HostID).CertSerial != serialBefore || e.host(idA.HostID).CertSerial != aBefore {
		t.Fatal("a refused renewal changed a host's certificate")
	}

	// A plain CSR is honoured (on a fresh session, since refused requests use
	// up a session's renewal budget), and the result is for host A, the
	// session's host.
	_, ok := csrWith(t)
	r := renewOn(t, rawStream(t, e, idA), ok)
	if r.GetError() != "" {
		t.Fatalf("legitimate renewal refused: %s", r.GetError())
	}
	got, err := pki.HostIDFromCert(certOf(t, r.GetCertificateDer()))
	if err != nil || got != idA.HostID {
		t.Fatalf("renewed certificate names %q (err %v), want host A %s", got, err, idA.HostID)
	}
	if e.host(idB.HostID).CertSerial != serialBefore {
		t.Fatal("host B's certificate changed")
	}
	if e.host(idA.HostID).CertSerial == aBefore {
		t.Fatal("host A's certificate did not change")
	}
}

// Malformed requests are rejected and leave the session and certificate alone.
func TestRenewalRejectsInvalidRequests(t *testing.T) {
	e := newEnv(t)
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("bad-csr"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serial := e.host(id.HostID).CertSerial
	_, good := csrWith(t)
	tampered := append([]byte(nil), good...)
	tampered[len(tampered)-3] ^= 0xff // breaks the CSR's signature
	for name, csr := range map[string][]byte{
		"empty":          nil,
		"garbage":        []byte("definitely not a CSR"),
		"bad signature":  tampered,
		"oversized junk": make([]byte, 64<<10),
	} {
		st := rawStream(t, e, id) // one session each: refusals use up the session's budget
		if r := renewOn(t, st, csr); r.GetError() == "" || len(r.GetCertificateDer()) != 0 {
			t.Errorf("%s: answered with %+v", name, r)
		}
	}
	if e.host(id.HostID).CertSerial != serial {
		t.Fatal("an invalid request changed the certificate")
	}
	// Invalid requests do not end a session: after one, the same stream
	// still gets answers (a second refusal here proves it is still served).
	st := rawStream(t, e, id)
	if r := renewOn(t, st, []byte("junk")); r.GetError() == "" {
		t.Fatal("junk accepted")
	}
	if r := renewOn(t, st, []byte("more junk")); r.GetError() == "" {
		t.Fatal("junk accepted")
	}
}

// After a renewal the replaced certificate works only until the new one first
// connects; then it is refused as superseded.
func TestReplacedCertificateIsRetiredOnceNewOneConnects(t *testing.T) {
	e := newEnv(t)
	old, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("retire"), e.pin(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, csr := csrWith(t)
	st := rawStream(t, e, old)
	r := renewOn(t, st, csr)
	if r.GetError() != "" {
		t.Fatal(r.GetError())
	}
	renewed := &agent.Identity{HostID: old.HostID, ServerURL: old.ServerURL, ServerName: old.ServerName, CADER: old.CADER, CertDER: r.GetCertificateDer(), Key: key}

	// Grace: the old certificate still connects (a lost reply must not lock the host out).
	rawStream(t, e, old)
	// The new certificate connects, which retires the old one.
	rawStream(t, e, renewed)

	cfg, _ := pki.ClientTLSConfig(old.CADER, old.CertDER, old.Key, "127.0.0.1")
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	defer tr.CloseIdleConnections()
	c := agentv1connect.NewAgentGatewayServiceClient(&http.Client{Transport: tr}, "https://"+e.addr)
	s2 := c.Connect(context.Background())
	_ = s2.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{}}})
	if _, err := s2.Receive(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("old certificate after the new one connected: err=%v, want permission_denied", err)
	}
}

func pkixName(cn string) pkix.Name { return pkix.Name{CommonName: cn} }
