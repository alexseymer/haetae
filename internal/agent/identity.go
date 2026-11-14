// Package agent is the host agent: it enrolls once and then keeps an
// outbound session to the control plane.
package agent

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	// renewedFile holds a renewed key and certificate together in one PEM
	// file, so a single rename switches both and a crash can never leave a
	// certificate next to the wrong key. It takes precedence over keyFile and
	// certFile when it is valid.
	renewedFile = "client.renewed.pem"
)

// LoadIdentity reads the identity from dir. It returns ErrNotEnrolled when
// dir has none.
func LoadIdentity(dir string) (*Identity, error) {
	raw, err := os.ReadFile(filepath.Join(dir, identityFile)) //nolint:gosec // G304: the agent's own state dir
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
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: the agent's own state dir
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
	id.useRenewed(dir)
	return &id, nil
}

// useRenewed switches id to the renewed key and certificate in dir, if there
// is a complete and consistent pair for this host. Anything else (missing,
// torn, another host, mismatched key) is ignored and the enrolled pair stays.
func (id *Identity) useRenewed(dir string) {
	raw, err := os.ReadFile(filepath.Join(dir, renewedFile)) //nolint:gosec // G304: the agent's own state dir
	if err != nil {
		return
	}
	var certDER []byte
	var key crypto.Signer
	for len(raw) > 0 {
		var blk *pem.Block
		blk, raw = pem.Decode(raw)
		if blk == nil {
			break
		}
		switch blk.Type {
		case "CERTIFICATE":
			certDER = blk.Bytes
		default:
			if k, err := pki.DecodeKeyPEM(pem.EncodeToMemory(blk)); err == nil {
				key = k
			}
		}
	}
	if certDER == nil || key == nil || checkCertKey(certDER, key, id.HostID) != nil {
		return
	}
	id.CertDER, id.Key = certDER, key
}

// checkCertKey verifies that certDER is a certificate for hostID whose public
// key belongs to key.
func checkCertKey(certDER []byte, key crypto.Signer, hostID string) error {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return err
	}
	if got, err := pki.HostIDFromCert(cert); err != nil || got != hostID {
		return errors.New("agent: certificate is for another host")
	}
	pub, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(key.Public()) {
		return errors.New("agent: certificate does not match the key")
	}
	return nil
}

// SaveRenewed persists a renewed certificate and its key in dir. The pair is
// written to one temporary file (mode 0600), synced, and renamed into place,
// so LoadIdentity sees either the old pair or the new one, never a mix.
func SaveRenewed(dir, hostID string, certDER []byte, key crypto.Signer) error {
	if err := checkCertKey(certDER, key, hostID); err != nil {
		return err
	}
	keyPEM, err := pki.EncodeKeyPEM(key)
	if err != nil {
		return err
	}
	data := append(keyPEM, pki.EncodeCertPEM(certDER)...)
	return writeAtomic(filepath.Join(dir, renewedFile), data, 0o600)
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
	// A fresh enrollment supersedes any renewed pair left from a previous one.
	if err := os.Remove(filepath.Join(dir, renewedFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agent: remove stale renewed identity: %w", err)
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
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // G304: the agent's own state dir
	if err != nil {
		return fmt.Errorf("agent: write %s: %w", filepath.Base(path), err)
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync() // the data must be on disk before the rename makes it live
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("agent: write %s: %w", filepath.Base(path), werr)
	}
	if err := os.Chmod(tmp, mode); err != nil { // O_CREATE honours the umask; a leftover file may differ
		_ = os.Remove(tmp)
		return fmt.Errorf("agent: chmod %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("agent: rename %s: %w", filepath.Base(path), err)
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil { //nolint:gosec // G304: the agent's own state dir
		_ = d.Sync() // best effort: make the rename itself durable
		_ = d.Close()
	}
	return nil
}
