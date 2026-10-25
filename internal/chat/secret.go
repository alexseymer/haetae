package chat

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	chatSecretFile = "chat.secret"
	chatSecretSize = 32
)

// LoadOrCreateChatSecret returns HAETAE_CHAT_SECRET (≥32 bytes) or loads /
// creates dataDir/chat.secret (32 random bytes, mode 0600). Never falls back
// to a hardcoded key. A short or unreadable env/file value is an error.
// Concurrent creators use O_CREATE|O_EXCL; the loser re-reads the winner's file.
func LoadOrCreateChatSecret(dataDir string) ([]byte, error) {
	if v := os.Getenv("HAETAE_CHAT_SECRET"); v != "" {
		b := []byte(v)
		if len(b) < chatSecretSize {
			return nil, fmt.Errorf("chat: HAETAE_CHAT_SECRET must be at least %d bytes", chatSecretSize)
		}
		return b, nil
	}
	if dataDir == "" {
		return nil, errors.New("chat: data dir required to persist chat secret")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("chat: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, chatSecretFile)
	return loadOrCreateChatSecretFile(path)
}

// LoadChatSecret is an alias kept for callers; prefer LoadOrCreateChatSecret.
func LoadChatSecret(dataDir string) ([]byte, error) {
	return LoadOrCreateChatSecret(dataDir)
}

func loadOrCreateChatSecretFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, chatSecretSize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("chat: generate secret: %w", err)
		}
		// Write the complete key to a private temp file, then publish it with
		// a hard link: the link is atomic and fails if the file exists, so a
		// concurrent starter never sees a partially written secret.
		tmp, err := os.CreateTemp(filepath.Dir(path), ".chat-secret-*") // 0600
		if err != nil {
			return nil, fmt.Errorf("chat: write secret: %w", err)
		}
		tmpName := tmp.Name()
		defer func() { _ = os.Remove(tmpName) }()
		_, werr := tmp.Write(key)
		if werr == nil {
			werr = tmp.Sync()
		}
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, fmt.Errorf("chat: write secret: %w", werr)
		}
		if err := os.Link(tmpName, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				// Another process won the create race; read its complete file.
				return readChatSecretFile(path)
			}
			return nil, fmt.Errorf("chat: write secret: %w", err)
		}
		fi, err = os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("chat: stat secret: %w", err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			_ = os.Remove(path)
			return nil, errors.New("chat: secret file is group/world accessible")
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chat: stat secret: %w", err)
	}
	return readChatSecretFileStat(path, fi)
}

func readChatSecretFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("chat: stat secret: %w", err)
	}
	return readChatSecretFileStat(path, fi)
}

func readChatSecretFileStat(path string, fi os.FileInfo) ([]byte, error) {
	if !fi.Mode().IsRegular() {
		return nil, errors.New("chat: secret path is not a regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("chat: secret file is group/world accessible")
	}
	key, err := os.ReadFile(path) //nolint:gosec // G304: operator data dir
	if err != nil {
		return nil, fmt.Errorf("chat: read secret: %w", err)
	}
	if len(key) < chatSecretSize {
		return nil, fmt.Errorf("chat: secret file has length %d, want at least %d", len(key), chatSecretSize)
	}
	return key, nil
}
