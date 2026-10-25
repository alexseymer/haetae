// Package secrets encrypts small secrets (Git tokens) at rest with a key
// kept in the data directory. The ciphertext is bound to an AAD string so a
// sealed value cannot be moved to another row.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	keyName   = "secret.key"
	keySize   = 32
	nonceSize = 12
)

// Box seals and opens small secrets with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// Open loads dir/secret.key, or creates it (32 random bytes, mode 0600) after
// creating dir at mode 0700. A key file with group/other access bits, or the
// wrong length, is refused.
func Open(dir string) (*Box, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secrets: create data dir: %w", err)
	}
	path := filepath.Join(dir, keyName)
	key, err := loadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, keySize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("secrets: generate key: %w", err)
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("secrets: write key: %w", err)
		}
		// Re-check mode in case the umask widened it.
		fi, err = os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("secrets: stat key: %w", err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			_ = os.Remove(path)
			return nil, errors.New("secrets: key file is group/world accessible")
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: stat key: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("secrets: key path is not a regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("secrets: key file is group/world accessible")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: read key: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: key file has length %d, want %d", len(key), keySize)
	}
	return key, nil
}

// Seal encrypts plain with a random 12-byte nonce prefix. aad is bound into
// the authentication tag (use the project id).
func (b *Box) Seal(plain []byte, aad string) ([]byte, error) {
	if b == nil {
		return nil, errors.New("secrets: nil box")
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	out := b.aead.Seal(nonce, nonce, plain, []byte(aad))
	return out, nil
}

// Open decrypts a Seal result. aad must match the value used to seal.
func (b *Box) Open(sealed []byte, aad string) ([]byte, error) {
	if b == nil {
		return nil, errors.New("secrets: nil box")
	}
	if len(sealed) < nonceSize {
		return nil, errors.New("secrets: ciphertext too short")
	}
	nonce, ct := sealed[:nonceSize], sealed[nonceSize:]
	plain, err := b.aead.Open(nil, nonce, ct, []byte(aad))
	if err != nil {
		return nil, errors.New("secrets: decryption failed")
	}
	return plain, nil
}
