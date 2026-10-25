package vuln

import (
	"context"
	"sync"
	"time"
)

// FakeMatcher is a pure Matcher for tests (no Grype).
type FakeMatcher struct {
	mu       sync.Mutex
	Findings []Finding
	BuiltAt  time.Time
	Err      error
	Calls    int
	LastSbom Sbom
}

// Match returns configured findings or Err.
func (f *FakeMatcher) Match(_ context.Context, sbom Sbom) ([]Finding, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	f.LastSbom = sbom
	if f.Err != nil {
		return nil, time.Time{}, f.Err
	}
	out := append([]Finding(nil), f.Findings...)
	built := f.BuiltAt
	if built.IsZero() {
		built = time.Now().UTC()
	}
	return out, built, nil
}

// SetFindings replaces the findings returned by Match.
func (f *FakeMatcher) SetFindings(findings []Finding, builtAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Findings = append([]Finding(nil), findings...)
	f.BuiltAt = builtAt
}
