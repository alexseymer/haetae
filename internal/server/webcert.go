package server

import (
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/alexseymer/haetae/internal/pki"
)

const (
	webCertTTL     = 2 * 365 * 24 * time.Hour
	webCertRenewIn = 30 * 24 * time.Hour
)

// LoadOrCreateWebCert returns the certificate for the web interface. It is
// kept in dir/web.crt and web.key and reused across restarts, so the public
// key pin shown in install commands stays valid; a new one is issued from ca
// when none exists, it expires within 30 days, or the names changed.
func LoadOrCreateWebCert(dir string, ca *pki.CA, names []string) (tls.Certificate, error) {
	crtPath, keyPath := filepath.Join(dir, "web.crt"), filepath.Join(dir, "web.key")
	if cert, err := tls.LoadX509KeyPair(crtPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil && webCertStillGood(leaf, ca, names) {
			cert.Leaf = leaf
			return cert, nil
		}
	}

	cert, err := ca.IssueServerCert(names, webCertTTL)
	if err != nil {
		return tls.Certificate{}, err
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return tls.Certificate{}, errors.New("server: issued key cannot sign")
	}
	keyPEM, err := pki.EncodeKeyPEM(signer)
	if err != nil {
		return tls.Certificate{}, err
	}
	var chain []byte
	for _, der := range cert.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	if err := os.WriteFile(crtPath, chain, 0o644); err != nil { //nolint:gosec // G306: a certificate is public
		return tls.Certificate{}, fmt.Errorf("server: write web.crt: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("server: write web.key: %w", err)
	}
	return cert, nil
}

func webCertStillGood(leaf *x509.Certificate, ca *pki.CA, names []string) bool {
	if time.Until(leaf.NotAfter) < webCertRenewIn {
		return false
	}
	if err := leaf.CheckSignatureFrom(ca.Cert); err != nil {
		return false // issued by a different CA
	}
	have := append(append([]string{}, leaf.DNSNames...), ipStrings(leaf)...)
	for _, n := range names {
		if !slices.Contains(have, n) {
			return false
		}
	}
	return true
}

func ipStrings(c *x509.Certificate) []string {
	out := make([]string, 0, len(c.IPAddresses))
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// PublicKeyPin returns the value for curl's --pinnedpubkey for a certificate:
// "sha256//" plus the base64 SHA-256 of its SubjectPublicKeyInfo.
func PublicKeyPin(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", errors.New("server: empty certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("server: parse certificate: %w", err)
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return "sha256//" + base64.StdEncoding.EncodeToString(sum[:]), nil
}
