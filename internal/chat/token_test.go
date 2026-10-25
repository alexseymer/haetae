package chat_test

import (
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

func TestActionTokenRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	id := uuid.New()
	exp := time.Now().Add(time.Hour)
	tok := store.SignActionToken(secret, id, "mute30d", exp)
	gotID, action, err := store.VerifyActionToken(secret, tok, time.Now())
	if err != nil || gotID != id || action != "mute30d" {
		t.Fatalf("verify: id=%v action=%q err=%v", gotID, action, err)
	}
}

func TestActionTokenForged(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	id := uuid.New()
	tok := store.SignActionToken(secret, id, "ack", time.Now().Add(time.Hour))
	tok = tok[:len(tok)-1] + "x"
	if _, _, err := store.VerifyActionToken(secret, tok, time.Now()); err == nil {
		t.Fatal("expected forged token rejection")
	}
}

func TestActionTokenExpired(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	id := uuid.New()
	tok := store.SignActionToken(secret, id, "later", time.Now().Add(-time.Minute))
	if _, _, err := store.VerifyActionToken(secret, tok, time.Now()); err == nil {
		t.Fatal("expected expired token rejection")
	}
}

func TestActionTokenHMACCompareRejectsWrongLengthMAC(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	id := uuid.New()
	tok := store.SignActionToken(secret, id, "ack", time.Now().Add(time.Hour))
	// Truncate the MAC segment so lengths differ; Verify must still reject
	// via digest-then-hmac.Equal (no early length panics / bypass).
	parts := splitToken(tok)
	if len(parts) != 2 {
		t.Fatal(tok)
	}
	short := parts[0] + "." + parts[1][:4]
	if _, _, err := store.VerifyActionToken(secret, short, time.Now()); err == nil {
		t.Fatal("expected rejection for wrong-length MAC")
	}
	emptyMAC := parts[0] + "."
	if _, _, err := store.VerifyActionToken(secret, emptyMAC, time.Now()); err == nil {
		t.Fatal("expected rejection for empty MAC")
	}
}

func splitToken(tok string) []string {
	for i := len(tok) - 1; i >= 0; i-- {
		if tok[i] == '.' {
			return []string{tok[:i], tok[i+1:]}
		}
	}
	return nil
}
