package chat

import (
	"testing"
	"time"
)

func TestTokenBucketMaxKeysEvictsOldest(t *testing.T) {
	m := newTokenBucketMap(5, time.Minute)
	m.maxKeys = 3
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !m.allow(string(rune('a'+i)), now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("allow %d", i)
		}
	}
	if m.lenForTest() != 3 {
		t.Fatalf("len=%d", m.lenForTest())
	}
	if !m.allow("d", now.Add(10*time.Second)) {
		t.Fatal("allow d")
	}
	if m.lenForTest() != 3 {
		t.Fatalf("after evict len=%d", m.lenForTest())
	}
	// Oldest ("a") should be gone.
	if m.buckets["a"] != nil {
		t.Fatal("expected oldest key evicted")
	}
}

func TestTokenBucketTTLSweep(t *testing.T) {
	m := newTokenBucketMap(5, time.Minute)
	m.idleTTL = time.Second
	now := time.Now()
	if !m.allow("x", now) {
		t.Fatal("allow")
	}
	if !m.allow("y", now.Add(2*time.Second)) {
		t.Fatal("allow y should sweep x")
	}
	if m.buckets["x"] != nil {
		t.Fatal("idle key not swept")
	}
}

func TestTokenBucketKeyNormalised(t *testing.T) {
	m := newTokenBucketMap(1, time.Hour)
	now := time.Now()
	if !m.allow("TG:1", now) {
		t.Fatal("first")
	}
	if m.allow(" tg:1 ", now) {
		t.Fatal("normalised key should share the bucket")
	}
}
