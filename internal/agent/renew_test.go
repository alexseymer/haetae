package agent

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/pki"
)

const testHost = "6f1c2f5e-6a52-4c3a-8a7e-0d8f5f0a1b11"

func enrolled(t *testing.T, ca *pki.CA, ttl time.Duration) (*Identity, string) {
	t.Helper()
	key, csr, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	der, _, err := ca.SignClientCSR(csr, testHost, ttl)
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{HostID: testHost, ServerURL: "https://x:1", ServerName: "x", CertDER: der, CADER: ca.DER, Key: key}
	dir := t.TempDir()
	if err := id.Save(dir); err != nil {
		t.Fatal(err)
	}
	return id, dir
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSaveRenewedIsPreferredAndPrivate(t *testing.T) {
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	id, dir := enrolled(t, ca, time.Hour)

	key, csr, _ := pki.NewKeyAndCSR()
	der, _, err := ca.SignClientCSR(csr, testHost, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRenewed(dir, testHost, der, key); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, renewedFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("renewed file: %v mode=%v, want 0600", err, fi)
	}
	if _, err := os.Stat(filepath.Join(dir, renewedFile+".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file left behind: %v", err)
	}
	got, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CertDER) != string(der) || got.Key.Public() == nil {
		t.Fatal("LoadIdentity did not pick up the renewed pair")
	}
	if string(got.CertDER) == string(id.CertDER) {
		t.Fatal("still the old certificate")
	}
}

func TestLoadIdentityIgnoresBadRenewedFile(t *testing.T) {
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	id, dir := enrolled(t, ca, time.Hour)
	path := filepath.Join(dir, renewedFile)

	otherKey, otherCSR, _ := pki.NewKeyAndCSR()
	_ = otherCSR
	wrongKeyKeyPEM, _ := pki.EncodeKeyPEM(otherKey)
	for name, data := range map[string][]byte{
		"torn":         []byte("-----BEGIN PRIVATE KEY-----\nMII"),
		"empty":        {},
		"key mismatch": append(wrongKeyKeyPEM, pki.EncodeCertPEM(id.CertDER)...),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadIdentity(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got.CertDER) != string(id.CertDER) {
			t.Errorf("%s: bad renewed file replaced the enrolled certificate", name)
		}
	}

	// A certificate for another host is refused even with its own key.
	k2, c2, _ := pki.NewKeyAndCSR()
	foreign, _, _ := ca.SignClientCSR(c2, "someone-else", time.Hour)
	if err := SaveRenewed(dir, testHost, foreign, k2); err == nil {
		t.Fatal("SaveRenewed accepted a certificate for another host")
	}
}

// fakeServer answers renewal requests as the gateway would, with ca.
type fakeServer struct {
	ca      *pki.CA
	ttl     time.Duration
	fail    int // refuse this many requests first
	reqs    int
	rn      *renewer
	badHost bool
}

func (f *fakeServer) send(msg *agentv1.ConnectRequest) error {
	rc := msg.GetRenewCertificate()
	if rc == nil {
		return nil
	}
	f.reqs++
	if f.fail > 0 {
		f.fail--
		go f.rn.deliver(&agentv1.RenewCertificateResponse{Error: "renewal failed"})
		return nil
	}
	host := testHost
	if f.badHost {
		host = "another-host"
	}
	der, _, err := f.ca.SignClientCSR(rc.GetCsrDer(), host, f.ttl)
	if err != nil {
		return err
	}
	go f.rn.deliver(&agentv1.RenewCertificateResponse{CertificateDer: der})
	return nil
}

func TestRenewerRenewsWhenDueAndPersists(t *testing.T) {
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	id, dir := enrolled(t, ca, 2*time.Hour)
	rn, err := newRenewer(id, dir, time.Hour, 20*time.Millisecond, quiet())
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeServer{ca: ca, ttl: 48 * time.Hour, rn: rn}
	if rn.due(time.Now()) {
		t.Fatal("2h left with a 1h window must not be due")
	}
	rn.renewBefore = 3 * time.Hour // now due
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rn.run(ctx, fs.send)

	deadline := time.Now().Add(5 * time.Second)
	for rn.due(time.Now()) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rn.due(time.Now()) {
		t.Fatal("certificate was not renewed")
	}
	time.Sleep(100 * time.Millisecond) // several checks: no further renewals while not due
	if fs.reqs != 1 {
		t.Fatalf("%d renewal requests, want exactly 1", fs.reqs)
	}
	// The next handshake presents the new certificate.
	c, _ := rn.clientCertificate(nil)
	if c.Leaf.NotAfter.Sub(time.Now()) < 40*time.Hour {
		t.Fatalf("presented cert expires in %s, want ~48h", time.Until(c.Leaf.NotAfter))
	}
	// And it is on disk for the next restart.
	got, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(got.CertDER)
	if cert.NotAfter.Sub(time.Now()) < 40*time.Hour {
		t.Fatal("renewed certificate not persisted")
	}
}

func TestRenewerRetriesAfterFailureAndKeepsOldCert(t *testing.T) {
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	id, dir := enrolled(t, ca, time.Hour)
	rn, _ := newRenewer(id, dir, time.Hour*2, 10*time.Millisecond, quiet())
	fs := &fakeServer{ca: ca, ttl: 48 * time.Hour, fail: 1, rn: rn}
	old, _ := rn.clientCertificate(nil)

	// First attempt is refused: old certificate stays, nothing is written.
	if err := rn.renewOnce(context.Background(), fs.send); err == nil {
		t.Fatal("refusal not reported")
	}
	cur, _ := rn.clientCertificate(nil)
	if string(cur.Certificate[0]) != string(old.Certificate[0]) {
		t.Fatal("certificate changed after a refused renewal")
	}
	if _, err := os.Stat(filepath.Join(dir, renewedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renewed file written after a refusal: %v", err)
	}
	// The run loop retries (backoff starts at one minute, so call again directly).
	if err := rn.renewOnce(context.Background(), fs.send); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestRenewerRejectsBadCertificates(t *testing.T) {
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	other, _ := pki.LoadOrCreateCA(t.TempDir())
	id, dir := enrolled(t, ca, time.Hour)
	rn, _ := newRenewer(id, dir, 2*time.Hour, time.Hour, quiet())

	for name, fs := range map[string]*fakeServer{
		"another host":   {ca: ca, ttl: 48 * time.Hour, badHost: true, rn: rn},
		"foreign CA":     {ca: other, ttl: 48 * time.Hour, rn: rn},
		"not an upgrade": {ca: ca, ttl: time.Minute, rn: rn},
	} {
		if err := rn.renewOnce(context.Background(), fs.send); err == nil {
			t.Errorf("%s: renewal accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, renewedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected certificate was persisted: %v", err)
	}
}

func TestRenewerNoAnswerOrSendErrorDoesNotPanic(t *testing.T) {
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	id, dir := enrolled(t, ca, time.Hour)
	rn, _ := newRenewer(id, dir, 2*time.Hour, time.Hour, quiet())
	err := rn.renewOnce(context.Background(), func(*agentv1.ConnectRequest) error { return errors.New("stream closed") })
	if err == nil {
		t.Fatal("send failure not reported")
	}
}
