package gateway

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/store"
)

// maxCSRBytes bounds a renewal CSR; a P-256 CSR is about 250 bytes.
const maxCSRBytes = 4096

// prevCertGrace is how long the certificate a renewal replaced stays valid
// when the new one has not been used yet. It covers a renewal reply that was
// lost or not saved; it ends as soon as the new certificate connects.
const prevCertGrace = 24 * time.Hour

// sessionCert is what the gateway keeps of the client certificate a session
// authenticated with.
type sessionCert struct {
	serial   string // as stored on the host row
	notAfter time.Time
	spki     []byte // DER SubjectPublicKeyInfo
}

// errRenewRefused wraps a refusal reason that is safe to tell the agent.
type errRenewRefused string

func (e errRenewRefused) Error() string { return string(e) }

// handleRenewCertificate answers a renewal request on the live session. The
// session stays up whatever the outcome; the answer goes out through the
// send loop (a stream allows only one sender).
//
// Every check below must pass, in this order:
//  1. rate limit per session;
//  2. the session's certificate is still within its validity;
//  3. the CSR parses, is signed by its own key, is small, and names no
//     other host;
//  4. the CSR key is not the session certificate's key (a fresh key);
//  5. the certificate is issued for s.hostID, taken from the verified
//     certificate at authentication, never from the message;
//  6. the database swap succeeds only if the host is not revoked and the
//     session's certificate is still the host's current (or just replaced)
//     one.
func (g *Gateway) handleRenewCertificate(ctx context.Context, s *session, req *agentv1.RenewCertificateRequest) {
	resp := &agentv1.RenewCertificateResponse{}
	der, err := g.renewCertificate(ctx, s, req.GetCsrDer())
	if err != nil {
		var refused errRenewRefused
		if errors.As(err, &refused) {
			resp.Error = refused.Error()
		} else {
			resp.Error = "renewal failed"
		}
		g.log.Warn("certificate renewal refused", "host_id", s.hostID, "err", err)
	} else {
		resp.CertificateDer = der
		g.log.Info("certificate renewed", "host_id", s.hostID)
	}
	select {
	case s.renew <- resp:
	case <-ctx.Done():
	}
}

func (g *Gateway) renewCertificate(ctx context.Context, s *session, csrDER []byte) ([]byte, error) {
	now := time.Now()
	if !s.renewLim.Allow() {
		return nil, errRenewRefused("renewal requests too frequent")
	}
	if !now.Before(s.cert.notAfter) {
		return nil, errRenewRefused("session certificate has expired")
	}
	if len(csrDER) == 0 || len(csrDER) > maxCSRBytes {
		return nil, errRenewRefused("certificate signing request is malformed")
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		return nil, errRenewRefused("certificate signing request is malformed")
	}
	if err := pki.CheckCSRIdentity(csr, s.hostID.String()); err != nil {
		return nil, errRenewRefused("certificate signing request names another identity")
	}
	if bytes.Equal(csr.RawSubjectPublicKeyInfo, s.cert.spki) {
		return nil, errRenewRefused("renewal requires a fresh key")
	}

	der, serial, err := g.ca.SignClientCSR(csrDER, s.hostID.String(), g.cfg.ClientCertTTL)
	if err != nil {
		return nil, errRenewRefused("certificate signing request is not acceptable")
	}
	issued, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	// Defence in depth: whatever the CA did, the certificate must name this
	// host and no other.
	if id, err := pki.HostIDFromCert(issued); err != nil || id != s.hostID.String() {
		return nil, errors.New("issued certificate does not carry the session's host id")
	}
	err = g.store.RenewHostCert(ctx, s.hostID, s.cert.serial, serial, issued.NotAfter, prevCertGrace)
	if errors.Is(err, store.ErrCertNotCurrent) {
		return nil, errRenewRefused("session certificate is not the host's current certificate")
	}
	if err != nil {
		return nil, err
	}
	return der, nil
}
