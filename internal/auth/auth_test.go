package auth

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// One test checks the real cost; the others use cheap hashing.
	os.Exit(m.Run())
}

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=1$") {
		t.Fatalf("unexpected format: %s", h)
	}
	if ok, err := VerifyPassword("correct horse battery", h); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword("correct horse batterY", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("two hashes of the same password are identical: no salt")
	}

	// Cheap test hashing still produces hashes that verify, and the real ones keep verifying.
	UseCheapHashingInTests()
	t.Cleanup(func() { argonTime, argonMemory = 2, 64*1024 })
	cheap, err := HashPassword("another passphrase")
	if err != nil || !strings.Contains(cheap, "m=8,t=1,p=1") {
		t.Fatalf("cheap hash: %v %q", err, cheap)
	}
	if ok, _ := VerifyPassword("another passphrase", cheap); !ok {
		t.Fatal("cheap hash does not verify")
	}
	if ok, _ := VerifyPassword("correct horse battery", h); !ok {
		t.Fatal("a real hash stopped verifying after switching to cheap hashing")
	}
}

func TestVerifyRefusesMaliciousOrBrokenHashes(t *testing.T) {
	for _, h := range []string{
		"", "plain", "$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaA", // 4 TiB of memory
		"$argon2id$v=19$m=65536,t=999,p=1$c2FsdHNhbHRzYWx0$aGFzaA",
		"$argon2id$v=19$m=65536,t=2,p=0$c2FsdHNhbHRzYWx0$aGFzaA",
		"$argon2i$v=19$m=65536,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaA",
		"$argon2id$v=19$m=65536,t=2,p=1$!!!$aGFzaA",
	} {
		start := time.Now()
		if ok, err := VerifyPassword("x", h); ok || err == nil {
			t.Errorf("VerifyPassword accepted or ignored %q: ok=%v err=%v", h, ok, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("rejecting %q took %v: the parameters were used", h, time.Since(start))
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                  false,
		"exactly12chr":           true,
		strings.Repeat("a", 12):  false,
		"Password1234":           false, // common (case-insensitive)
		"alice@example.com":      false, // equals the email below
		strings.Repeat("x", 129): false,
		"a sensible passphrase":  true,
	} {
		err := CheckPassword(pw, "alice@example.com")
		if (err == nil) != ok {
			t.Errorf("CheckPassword(%q) = %v, want ok=%v", pw, err, ok)
		}
	}
}

func TestTokens(t *testing.T) {
	a, ha, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := NewToken()
	if a == b || len(a) < 43 {
		t.Fatalf("tokens not random enough: %q %q", a, b)
	}
	if string(HashToken(a)) != string(ha) || string(HashToken(b)) == string(ha) {
		t.Fatal("HashToken inconsistent")
	}
	if strings.Contains(string(ha), a) {
		t.Fatal("hash contains the token")
	}
}

func TestLimiterWindowAndReset(t *testing.T) {
	now := time.Now()
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for range 3 {
		if ok, _ := l.Allowed("k"); !ok {
			t.Fatal("blocked too early")
		}
		l.Fail("k")
	}
	ok, retry := l.Allowed("k")
	if ok || retry <= 0 || retry > time.Minute {
		t.Fatalf("after 3 failures: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allowed("other"); !ok {
		t.Fatal("another key is blocked")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.Allowed("k"); !ok {
		t.Fatal("still blocked after the window")
	}
	l.Fail("k")
	l.Reset("k")
	if len(l.fails) != 0 {
		t.Fatal("Reset left state behind")
	}
}

func TestLimiterIsBounded(t *testing.T) {
	l := NewLimiter(5, time.Hour)
	for i := range 3 * maxLimiterKeys {
		l.Fail(strings.Repeat("k", 1) + string(rune('a'+i%26)) + string(rune(i)))
	}
	if len(l.fails) > maxLimiterKeys {
		t.Fatalf("limiter holds %d keys, want <= %d", len(l.fails), maxLimiterKeys)
	}
}

func TestNewCode(t *testing.T) {
	c, err := NewCode(12)
	if err != nil || len(c) != 12 || strings.ContainsAny(c, "il1o0") {
		t.Fatalf("code %q err %v", c, err)
	}
}
