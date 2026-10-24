package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexseymer/haetae/internal/pki"
)

func TestIdentityRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := LoadIdentity(dir); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty dir: err = %v, want ErrNotEnrolled", err)
	}

	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, csr, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := ca.SignClientCSR(csr, "host-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := &Identity{HostID: "host-1", ServerURL: "https://x:8443", ServerName: "x", CertDER: cert, CADER: ca.DER, Key: key}
	if err := id.Save(dir); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}

	got, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostID != id.HostID || got.ServerURL != id.ServerURL || string(got.CertDER) != string(cert) || string(got.CADER) != string(ca.DER) {
		t.Fatalf("loaded identity differs: %+v", got)
	}
}
