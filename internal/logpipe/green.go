package logpipe

// GreenSet is an immutable set of template IDs judged routine. It is safe to
// share between aggregators and goroutines; a nil *GreenSet is empty.
type GreenSet struct {
	m map[string]struct{}
}

// NewGreenSet copies ids into a set.
func NewGreenSet(ids []string) *GreenSet {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return &GreenSet{m: m}
}

// Has reports whether id is in the set.
func (g *GreenSet) Has(id string) bool {
	if g == nil {
		return false
	}
	_, ok := g.m[id]
	return ok
}

// Len returns the number of ids.
func (g *GreenSet) Len() int {
	if g == nil {
		return 0
	}
	return len(g.m)
}
