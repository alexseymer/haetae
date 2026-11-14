package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/store"
)

type renewStore struct {
	Store
	calls   int
	from    string
	newSer  string
	expires time.Time
	err     error
}

func (r *renewStore) RenewHostCert(_ context.Context, _ uuid.UUID, from, newSerial string, expires time.Time, _ time.Duration) error {
	r.calls++
	r.from, r.newSer, r.expires = from, newSerial, expires
	return r.err
}

func renewFixture(t *testing.T, notAfter time.Time) (*Gateway, *renewStore, *session, *ecdsa.PrivateKey) {
	t.Helper()
	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := &renewStore{}
	g := New(st, ca, Config{ClientCertTTL: 48 * time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	key, _, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(uuid.New(), func(error) {})
	s.cert = sessionCert{serial: "abc", notAfter: notAfter, spki: spki}
	return g, st, s, key
}

func csrFor(t *testing.T, tmpl *x509.CertificateRequest) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestRenewIssuesForSessionHostOnly(t *testing.T) {
	g, st, s, _ := renewFixture(t, time.Now().Add(time.Hour))
	other := uuid.New()
	// The CSR carries no host claim: the certificate must still name s.hostID.
	der, err := g.renewCertificate(context.Background(), s, csrFor(t, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "haetae-agent"}}))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := pki.HostIDFromCert(cert); id != s.hostID.String() || id == other.String() {
		t.Fatalf("issued for %q, want %s", id, s.hostID)
	}
	if st.calls != 1 || st.from != "abc" || !st.expires.Equal(cert.NotAfter) {
		t.Fatalf("store call: %+v", st)
	}
	if ttl := time.Until(cert.NotAfter); ttl < 47*time.Hour || ttl > 49*time.Hour {
		t.Fatalf("ttl %s, want the configured 48h", ttl)
	}
}

func TestRenewRefusals(t *testing.T) {
	otherURI, _ := url.Parse("haetae://host/" + uuid.NewString())
	for _, tc := range []struct {
		name     string
		notAfter time.Duration
		csr      func(t *testing.T, s *session, key *ecdsa.PrivateKey) []byte
		storeErr error
	}{
		{"csr names another host by URI", time.Hour, func(t *testing.T, _ *session, _ *ecdsa.PrivateKey) []byte {
			return csrFor(t, &x509.CertificateRequest{URIs: []*url.URL{otherURI}})
		}, nil},
		{"csr names another host by CN", time.Hour, func(t *testing.T, _ *session, _ *ecdsa.PrivateKey) []byte {
			return csrFor(t, &x509.CertificateRequest{Subject: pkix.Name{CommonName: uuid.NewString()}})
		}, nil},
		{"garbage csr", time.Hour, func(*testing.T, *session, *ecdsa.PrivateKey) []byte { return []byte("not a csr") }, nil},
		{"empty csr", time.Hour, func(*testing.T, *session, *ecdsa.PrivateKey) []byte { return nil }, nil},
		{"same key reused", time.Hour, func(t *testing.T, _ *session, key *ecdsa.PrivateKey) []byte {
			der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
			if err != nil {
				t.Fatal(err)
			}
			return der
		}, nil},
		{"session certificate expired", -time.Second, func(t *testing.T, _ *session, _ *ecdsa.PrivateKey) []byte {
			return csrFor(t, &x509.CertificateRequest{})
		}, nil},
		{"certificate no longer current", time.Hour, func(t *testing.T, _ *session, _ *ecdsa.PrivateKey) []byte {
			return csrFor(t, &x509.CertificateRequest{})
		}, store.ErrCertNotCurrent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, st, s, key := renewFixture(t, time.Now().Add(tc.notAfter))
			st.err = tc.storeErr
			der, err := g.renewCertificate(context.Background(), s, tc.csr(t, s, key))
			var refused errRenewRefused
			if err == nil || !errors.As(err, &refused) || der != nil {
				t.Fatalf("want a refusal, got der=%d err=%v", len(der), err)
			}
			if tc.storeErr == nil && st.calls != 0 {
				t.Fatalf("store was called %d times for a refused request", st.calls)
			}
		})
	}
}

func TestRenewRateLimited(t *testing.T) {
	g, _, s, _ := renewFixture(t, time.Now().Add(time.Hour))
	var refused errRenewRefused
	for i := 0; i < 3; i++ {
		if _, err := g.renewCertificate(context.Background(), s, csrFor(t, &x509.CertificateRequest{})); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := g.renewCertificate(context.Background(), s, csrFor(t, &x509.CertificateRequest{})); !errors.As(err, &refused) {
		t.Fatalf("4th request in a burst: %v, want refusal", err)
	}
}

func TestRenewStoreFailureIsGenericToAgent(t *testing.T) {
	g, st, s, _ := renewFixture(t, time.Now().Add(time.Hour))
	st.err = errors.New("pq: connection refused at 10.0.0.5")
	s.renew = make(chan *agentv1.RenewCertificateResponse, 1)
	g.handleRenewCertificate(context.Background(), s, &agentv1.RenewCertificateRequest{CsrDer: csrFor(t, &x509.CertificateRequest{})})
	resp := <-s.renew
	if resp.GetError() != "renewal failed" || len(resp.GetCertificateDer()) != 0 {
		t.Fatalf("response: %+v", resp)
	}
}
