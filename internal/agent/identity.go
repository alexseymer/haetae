// Package agent is the host agent: it enrolls once and then keeps an
// outbound session to the control plane.
package agent

import (
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexseymer/haetae/internal/pki"
)

// ErrNotEnrolled means the state directory holds no identity.
var ErrNotEnrolled = errors.New("agent: not enrolled")

// Identity is what the agent keeps between restarts.
type Identity struct {
	HostID     string `json:"host_id"`
	ServerURL  string `json:"server_url"`
	ServerName string `json:"server_name"`

	CertDER []byte        `json:"-"`
	CADER   []byte        `json:"-"`
	Key     crypto.Signer `json:"-"`
}

const (
	identityFile = "identity.json"
	certFile     = "client.crt"
	keyFile      = "client.key"
	caFile       = "ca.crt"
)

// LoadIdentity reads the identity from dir. It returns ErrNotEnrolled when
// dir has none.
func LoadIdentity(dir string) (*Identity, error) {
	raw, err := os.ReadFile(filepath.Join(dir, identityFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, fmt.Errorf("agent: read identity: %w", err)
	}

	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("agent: parse identity: %w", err)
	}

	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("agent: read %s: %w", name, err)
		}
		return b, nil
	}
	certPEM, err := read(certFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := read(caFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := read(keyFile)
	if err != nil {
		return nil, err
	}

	if id.CertDER, err = pki.DecodeCertPEM(certPEM); err != nil {
		return nil, err
	}
	if id.CADER, err = pki.DecodeCertPEM(caPEM); err != nil {
		return nil, err
	}
	if id.Key, err = pki.DecodeKeyPEM(keyPEM); err != nil {
		return nil, err
	}
	return &id, nil
}

// Save writes the identity to dir. Files are written to temporary names and
// renamed, and identity.json last, so a crash never leaves a half identity
// that LoadIdentity would accept.
func (id *Identity) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("agent: create state dir: %w", err)
	}
	keyPEM, err := pki.EncodeKeyPEM(id.Key)
	if err != nil {
		return err
	}
	meta, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("agent: encode identity: %w", err)
	}

	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{keyFile, keyPEM, 0o600},
		{certFile, pki.EncodeCertPEM(id.CertDER), 0o644},
		{caFile, pki.EncodeCertPEM(id.CADER), 0o644},
		{identityFile, meta, 0o600},
	}
	for _, f := range files {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("agent: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("agent: rename %s: %w", filepath.Base(path), err)
	}
	return nil
}
