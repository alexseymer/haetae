package chat

import (
	"sync"
	"time"
)

// tokenBucketMap rate-limits replies per key (e.g. channel:external_id).
type tokenBucketMap struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
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
		buckets: make(map[string]*tokenBucket),
	}
}

func (m *tokenBucketMap) allow(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[key]
	if b == nil {
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
