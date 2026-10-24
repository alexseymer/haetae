package logpipe

import (
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRingBounds(t *testing.T) {
	r := NewRing(3, 100)
	base := time.Unix(0, 0).UTC()
	r.Add(base.Add(1*time.Second), "one")
	r.Add(base.Add(2*time.Second), "two")
	r.Add(base.Add(3*time.Second), "three")
	r.Add(base.Add(4*time.Second), "four")
	if r.Len() != 3 {
		t.Fatalf("Len=%d want 3", r.Len())
	}
	q := r.Query(base, base.Add(10*time.Second), 10)
	if len(q) != 3 || q[0].Line != "two" || q[2].Line != "four" {
		t.Fatalf("query=%v", q)
	}
}

func TestRingByteBound(t *testing.T) {
	r := NewRing(100, 10)
	base := time.Now()
	r.Add(base, "abcdefghij") // 10 bytes exactly
	if r.Len() != 1 || r.Bytes() != 10 {
		t.Fatalf("len=%d bytes=%d", r.Len(), r.Bytes())
	}
	r.Add(base.Add(time.Second), "xyz") // forces eviction
	if r.Bytes() > 10 {
		t.Fatalf("bytes=%d > 10", r.Bytes())
	}
	if r.Len() != 1 || r.Query(base, base.Add(2*time.Second), 10)[0].Line != "xyz" {
		t.Fatalf("unexpected state len=%d", r.Len())
	}
}

func TestRingTruncateLongLine(t *testing.T) {
	r := NewRing(10, 8)
	line := "こんにちは世界" // multi-byte
	r.Add(time.Now(), line)
	if r.Len() != 1 {
		t.Fatal("expected one entry")
	}
	got := r.Query(time.Time{}, time.Now().Add(time.Hour), 1)[0].Line
	if len(got) > 8 {
		t.Fatalf("len=%d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid utf8 after truncate")
	}
}

func TestRingQueryMax(t *testing.T) {
	r := NewRing(10, 1000)
	base := time.Unix(100, 0).UTC()
	for i := 0; i < 5; i++ {
		r.Add(base.Add(time.Duration(i)*time.Second), "x")
	}
	q := r.Query(base, base.Add(10*time.Second), 2)
	if len(q) != 2 {
		t.Fatalf("len=%d", len(q))
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing(200, 1<<20)
	base := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.Add(base.Add(time.Duration(g*200+i)*time.Millisecond), "line")
				_ = r.Query(base, base.Add(time.Hour), 50)
				_ = r.Len()
				_ = r.Bytes()
			}
		}(g)
	}
	wg.Wait()
	if r.Len() > 200 {
		t.Fatalf("Len=%d > 200", r.Len())
	}
	if r.Bytes() > 1<<20 {
		t.Fatalf("Bytes=%d", r.Bytes())
	}
}
