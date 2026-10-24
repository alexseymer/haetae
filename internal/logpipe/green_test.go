package logpipe

import (
	"testing"
	"time"
)

func TestSharedGreenSetAndNilSafety(t *testing.T) {
	var nilSet *GreenSet
	if nilSet.Has("x") || nilSet.Len() != 0 {
		t.Fatal("nil GreenSet must be empty")
	}

	line := "GET /api/items/1 200 12ms level=info"
	id := TemplateID(DetectLevel(line), Template(line))
	shared := NewGreenSet([]string{id})

	now := time.Now()
	for i := 0; i < 2; i++ { // two aggregators, one set
		a := NewAggregator(Config{})
		a.SetGreenSet(shared)
		if !a.Add(line, now) {
			t.Fatalf("aggregator %d did not skip a green info line", i)
		}
	}
}
