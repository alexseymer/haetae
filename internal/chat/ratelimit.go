package chat

import (
	"strings"
	"sync"
	"time"
)

const (
	rateLimitMaxKeys = 10_000
	rateLimitIdleTTL = time.Hour
)

// tokenBucketMap rate-limits replies per key (e.g. channel:external_id).
type tokenBucketMap struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	maxKeys int
	idleTTL time.Duration
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newTokenBucketMap(burst int, per time.Duration) *tokenBucketMap {
	if burst < 1 {
		burst = 1
	}
	if per <= 0 {
		per = time.Minute
	}
	return &tokenBucketMap{
		rate:    float64(burst) / per.Seconds(),
		burst:   float64(burst),
		maxKeys: rateLimitMaxKeys,
		idleTTL: rateLimitIdleTTL,
		buckets: make(map[string]*tokenBucket),
	}
}

func normalizeRateKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func (m *tokenBucketMap) allow(key string, now time.Time) bool {
	key = normalizeRateKey(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(now)
	b := m.buckets[key]
	if b == nil {
		m.evictOldestLocked()
		b = &tokenBucket{tokens: m.burst, last: now}
		m.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * m.rate
		if b.tokens > m.burst {
			b.tokens = m.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (m *tokenBucketMap) sweepLocked(now time.Time) {
	if m.idleTTL <= 0 {
		return
	}
	for k, b := range m.buckets {
		if now.Sub(b.last) > m.idleTTL {
			delete(m.buckets, k)
		}
	}
}

func (m *tokenBucketMap) evictOldestLocked() {
	if m.maxKeys <= 0 || len(m.buckets) < m.maxKeys {
		return
	}
	var oldestKey string
	var oldest time.Time
	first := true
	for k, b := range m.buckets {
		if first || b.last.Before(oldest) {
			oldestKey = k
			oldest = b.last
			first = false
		}
	}
	if oldestKey != "" {
		delete(m.buckets, oldestKey)
	}
}

func (m *tokenBucketMap) lenForTest() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.buckets)
}
