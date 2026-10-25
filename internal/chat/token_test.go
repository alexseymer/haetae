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
