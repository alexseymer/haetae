package server

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexseymer/haetae/internal/pki"
)

func TestWebCertificateSurvivesRestartsWithAStablePin(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"localhost", "127.0.0.1", "haetae.example.com"}

	first, err := LoadOrCreateWebCert(dir, ca, names)
	if err != nil {
		t.Fatal(err)
	}
	pin1, err := PublicKeyPin(first)
	if err != nil {
		t.Fatal(err)
	}
	// "Restart": the same files give the same certificate and pin.
	second, err := LoadOrCreateWebCert(dir, ca, names)
	if err != nil {
		t.Fatal(err)
	}
	if pin2, _ := PublicKeyPin(second); pin2 != pin1 {
		t.Fatalf("pin changed across restarts: %s -> %s; install commands copied earlier would break", pin1, pin2)
	}
	// It is signed by the CA, covers the names, and the key file is private.
	leaf, _ := x509.ParseCertificate(second.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "haetae.example.com"}); err != nil {
		t.Fatalf("certificate does not verify: %v", err)
	}
	if info := mustStat(t, filepath.Join(dir, "web.key")); info != 0o600 {
		t.Fatalf("web.key mode = %v, want 0600", info)
	}

	// A new name is not covered by the old certificate: a new one is issued.
	third, err := LoadOrCreateWebCert(dir, ca, append(names, "new.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if pin3, _ := PublicKeyPin(third); pin3 == pin1 {
		t.Fatal("a certificate lacking the new name was reused")
	}

	// A different CA (data directory reset) also forces a new certificate.
	otherCA, _ := pki.LoadOrCreateCA(t.TempDir())
	fourth, err := LoadOrCreateWebCert(dir, otherCA, append(names, "new.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	leaf4, _ := x509.ParseCertificate(fourth.Certificate[0])
	if err := leaf4.CheckSignatureFrom(otherCA.Cert); err != nil {
		t.Fatalf("certificate not issued by the current CA: %v", err)
	}
}

func mustStat(t *testing.T, path string) (mode uint32) {
	t.Helper()
	info, err := osStat(path)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(info.Mode().Perm())
}

var osStat = os.Stat
