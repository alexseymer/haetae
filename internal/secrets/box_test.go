package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("ghp_canary_token_value")
	aad := "11111111-1111-1111-1111-111111111111"
	sealed, err := b.Seal(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Open(sealed, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}
}

func TestTamper(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal([]byte("secret"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := b.Open(sealed, "aad"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestWrongAAD(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal([]byte("secret"), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(sealed, "project-b"); err == nil {
		t.Fatal("wrong AAD accepted")
	}
}

func TestKeyPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, keyName)
	if err := os.WriteFile(path, make([]byte, keySize), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("group-readable key accepted")
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, keyName), make([]byte, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir2); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestKeyReuseAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	b1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b1.Seal([]byte("x"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b2.Open(sealed, "aad")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Fatalf("got %q", got)
	}
}

func TestNonceUniqueness(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for range 200 {
		sealed, err := b.Seal([]byte("same"), "aad")
		if err != nil {
			t.Fatal(err)
		}
		n := string(sealed[:nonceSize])
		if _, ok := seen[n]; ok {
			t.Fatal("duplicate nonce")
		}
		seen[n] = struct{}{}
	}
}
