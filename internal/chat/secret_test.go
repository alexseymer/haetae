package chat_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexseymer/haetae/internal/chat"
)

func TestChatSecretDifferentDirs(t *testing.T) {
	t.Setenv("HAETAE_CHAT_SECRET", "")
	_ = os.Unsetenv("HAETAE_CHAT_SECRET")

	d1 := t.TempDir()
	d2 := t.TempDir()
	s1, err := chat.LoadOrCreateChatSecret(d1)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := chat.LoadOrCreateChatSecret(d2)
	if err != nil {
		t.Fatal(err)
	}
	if string(s1) == string(s2) {
		t.Fatal("expected different secrets for different data dirs")
	}
	// Persisted 0600.
	fi, err := os.Stat(filepath.Join(d1, "chat.secret"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	// Reload same dir → same secret.
	s1b, err := chat.LoadOrCreateChatSecret(d1)
	if err != nil || string(s1b) != string(s1) {
		t.Fatalf("reload: %v %q vs %q", err, s1b, s1)
	}
}

func TestChatSecretEnvTooShort(t *testing.T) {
	t.Setenv("HAETAE_CHAT_SECRET", "tooshort")
	if _, err := chat.LoadOrCreateChatSecret(t.TempDir()); err == nil {
		t.Fatal("expected error for short env secret")
	}
}

func TestChatSecretFileTooShort(t *testing.T) {
	_ = os.Unsetenv("HAETAE_CHAT_SECRET")
	d := t.TempDir()
	path := filepath.Join(d, "chat.secret")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.LoadOrCreateChatSecret(d); err == nil {
		t.Fatal("expected error for short secret file")
	}
}

func TestValidatePublicURLViaEnvSecretOK(t *testing.T) {
	t.Setenv("HAETAE_CHAT_SECRET", "0123456789abcdef0123456789abcdef")
	s, err := chat.LoadOrCreateChatSecret(t.TempDir())
	if err != nil || len(s) < 32 {
		t.Fatalf("%v len=%d", err, len(s))
	}
}
