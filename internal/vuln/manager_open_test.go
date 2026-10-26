package vuln

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

type closeMatcher struct {
	closed atomic.Bool
}

func (c *closeMatcher) Match(context.Context, Sbom) ([]Finding, time.Time, error) {
	return nil, time.Now(), nil
}
func (c *closeMatcher) Close() error { c.closed.Store(true); return nil }

// The old matcher must be closed before the new one is opened (a reader opened
// at the same path while the replaced file is still open sees the old data),
// and a database that fails to open leaves the manager not ready.
func TestOpenActiveClosesOldBeforeOpeningNew(t *testing.T) {
	dir := t.TempDir()
	active := grypedb.ActiveDBDir(dir)
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "vulnerability.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, second := &closeMatcher{}, &closeMatcher{}
	calls := 0
	var firstClosedAtSecondOpen bool
	m := NewManager(nil, Config{ListingURL: "http://x", DataDir: dir, OpenMatcher: func(string) (Matcher, error) {
		calls++
		switch calls {
		case 1:
			return first, nil
		case 2:
			firstClosedAtSecondOpen = first.closed.Load()
			return nil, errors.New("corrupt database")
		}
		return second, nil
	}}, nil, nil)

	if err := m.openActive(); err != nil || !m.Ready() {
		t.Fatalf("first open: ready=%v err=%v", m.Ready(), err)
	}
	if err := m.openActive(); err == nil {
		t.Fatal("want error from corrupt database")
	}
	if !firstClosedAtSecondOpen {
		t.Fatal("old matcher was still open when the new database was opened")
	}
	if m.Ready() {
		t.Fatal("manager must not be ready after a failed open")
	}
	if err := m.openActive(); err != nil || !m.Ready() || m.Matcher() != Matcher(second) {
		t.Fatalf("recovery open: ready=%v err=%v", m.Ready(), err)
	}
}

type pendingStore struct {
	Store
	rows []struct {
		OrgID   uuid.UUID
		ImageID string
	}
	matched []string
}

func (p *pendingStore) ListUnmatchedSboms(context.Context, uuid.UUID, string, int) ([]struct {
	OrgID   uuid.UUID
	ImageID string
}, error) {
	rows := p.rows
	p.rows = nil
	return rows, nil
}
func (p *pendingStore) LoadSbom(_ context.Context, org uuid.UUID, img string) (store.Sbom, error) {
	return store.Sbom{OrgID: org, ImageID: img}, nil
}
func (p *pendingStore) ReplaceFindings(context.Context, uuid.UUID, string, []store.Finding) error {
	return nil
}
func (p *pendingStore) SetMatchState(_ context.Context, _ uuid.UUID, img, status, _ string, _ *time.Time) error {
	if status == store.MatchOK {
		p.matched = append(p.matched, img)
	}
	return nil
}

func TestRematchPendingMatchesDroppedImages(t *testing.T) {
	org := uuid.New()
	st := &pendingStore{rows: []struct {
		OrgID   uuid.UUID
		ImageID string
	}{{org, "dropped1"}, {org, "dropped2"}}}
	nudges := 0
	m := NewManager(st, Config{ListingURL: "http://x", DataDir: t.TempDir()}, nil, func() { nudges++ })
	if err := m.RematchPending(context.Background()); err != nil || len(st.matched) != 0 {
		t.Fatalf("must be a no-op until the matcher is ready: %v %v", err, st.matched)
	}
	st.rows = []struct {
		OrgID   uuid.UUID
		ImageID string
	}{{org, "dropped1"}, {org, "dropped2"}}
	m.SetMatcherForTest(&closeMatcher{})
	if err := m.RematchPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.matched) != 2 || nudges != 1 {
		t.Fatalf("matched=%v nudges=%d", st.matched, nudges)
	}
}
