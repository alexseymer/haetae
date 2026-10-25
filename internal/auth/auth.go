// Package auth hashes passwords, makes and checks session tokens, and limits
// sign-in attempts.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Memory is the main cost: 64 MiB per hash, with at most
// maxConcurrentHashes running at once (so at most 128 MiB at a time).
const (
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16

	maxConcurrentHashes = 2
	maxArgonMemory      = 4 * 64 * 1024 // KiB: verification refuses hashes asking for more
)

// Cost of new hashes. Fixed in production; see UseCheapHashingInTests.
var (
	argonTime   uint32 = 2
	argonMemory uint32 = 64 * 1024 // KiB
)

// UseCheapHashingInTests makes new hashes nearly free so tests that create
// users stay fast. It panics outside a test binary, so it cannot weaken a
// running server. Existing hashes keep verifying with their own parameters.
func UseCheapHashingInTests() {
	if !testing.Testing() {
		panic("auth: UseCheapHashingInTests called outside a test")
	}
	argonTime, argonMemory = 1, 8
}

var hashSlots = make(chan struct{}, maxConcurrentHashes)

// HashPassword returns an encoded Argon2id hash of password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	hashSlots <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSlots
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches an encoded hash, in constant
// time for the final comparison.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: unsupported hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("auth: unsupported argon2 version")
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false, errors.New("auth: bad argon2 parameters")
	}
	// Refuse parameters far above ours: a tampered hash must not make the
	// server allocate gigabytes.
	if mem < 8 || mem > maxArgonMemory || t < 1 || t > 10 || p == 0 || p > 8 {
		return false, errors.New("auth: argon2 parameters out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("auth: bad salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false, errors.New("auth: bad hash")
	}
	hashSlots <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want))) //nolint:gosec // G115: len bounded above
	<-hashSlots
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// DummyHash is a valid hash of a random password, verified against when the
// user does not exist so that unknown and known users take the same time. It
// is made on first use with the current cost.
func DummyHash() string {
	dummyOnce.Do(func() {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		h, err := HashPassword(base64.RawStdEncoding.EncodeToString(b))
		if err != nil {
			panic(err)
		}
		dummyHash = h
	})
	return dummyHash
}

// Password policy limits.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 128 // bytes; bounds hashing work
)

// ErrPassword describes why a password was refused; it is safe to show.
type ErrPassword string

func (e ErrPassword) Error() string { return string(e) }

var commonPasswords = map[string]bool{
	"password1234": true, "123456789012": true, "qwertyuiop12": true, "administrator": true,
	"letmein12345": true, "changeme1234": true, "passwordpassword": true, "haetae123456": true,
}

// CheckPassword applies the password policy.
func CheckPassword(password, email string) error {
	switch {
	case len(password) < MinPasswordLen:
		return ErrPassword(fmt.Sprintf("password must be at least %d characters", MinPasswordLen))
	case len(password) > MaxPasswordLen:
		return ErrPassword(fmt.Sprintf("password must be at most %d bytes", MaxPasswordLen))
	case strings.EqualFold(password, email):
		return ErrPassword("password must differ from the email address")
	case commonPasswords[strings.ToLower(password)]:
		return ErrPassword("password is too common")
	case strings.Count(password, string(password[0])) == len(password):
		return ErrPassword("password must not be one repeated character")
	}
	return nil
}

// NewToken returns a random session token for the cookie and the hash to
// store. Only the hash is kept server-side.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the SHA-256 of a session token.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// NewCode returns a random code of n lowercase letters and digits, readable
// aloud (no look-alike characters), for the first-run setup code.
func NewCode(n int) (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	// Rejection sampling: bytes at or above the largest multiple of the
	// alphabet size are discarded, so every character is equally likely.
	limit := byte(256 - 256%len(alphabet))
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("auth: code: %w", err)
		}
		for _, b := range buf {
			if b < limit && len(out) < n {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

// Limiter counts failed sign-ins per key in a sliding window. Safe for
// concurrent use. Memory is bounded: when it holds too many keys it drops the
// stale ones.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	fails     map[string][]time.Time
	lastSweep time.Time
}

const maxLimiterKeys = 10000

// NewLimiter allows max failures per key within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, fails: make(map[string][]time.Time)}
}

// Allowed reports whether key may try again, and if not, how long to wait.
func (l *Limiter) Allowed(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	times := l.prune(key)
	if len(times) < l.max {
		return true, 0
	}
	return false, times[0].Add(l.window).Sub(l.now())
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) >= maxLimiterKeys {
		// Full. Sweeping all keys is O(n), so do it at most once a minute (an
		// attacker filling the table must not turn every attempt into a sweep);
		// in between, evict one arbitrary entry to make room.
		if now := l.now(); now.Sub(l.lastSweep) > time.Minute {
			l.lastSweep = now
			for k := range l.fails {
				if len(l.prune(k)) == 0 {
					delete(l.fails, k)
				}
			}
		}
		for k := range l.fails {
			if len(l.fails) < maxLimiterKeys {
				break
			}
			delete(l.fails, k)
		}
	}
	l.fails[key] = append(l.prune(key), l.now())
}

// Reset forgets a key, after a successful sign-in.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}

func (l *Limiter) prune(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	times := l.fails[key]
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	if i > 0 {
		times = append([]time.Time(nil), times[i:]...)
		if len(times) == 0 {
			delete(l.fails, key)
			return nil
		}
		l.fails[key] = times
	}
	return times
}
