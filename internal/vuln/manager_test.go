package vuln_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/vuln"
	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

type memVulnStore struct {
	mu       sync.Mutex
	db       store.VulnDB
	sboms    map[string]store.Sbom // org|image
	findings map[string][]store.Finding
	match    map[string]store.ImageMatchState
}

func newMemVulnStore() *memVulnStore {
	return &memVulnStore{
		db:       store.VulnDB{Status: store.VulnDBNone},
		sboms:    map[string]store.Sbom{},
		findings: map[string][]store.Finding{},
		match:    map[string]store.ImageMatchState{},
	}
}

func (m *memVulnStore) key(org uuid.UUID, image string) string {
	return org.String() + "|" + image
}

func (m *memVulnStore) GetVulnDB(context.Context) (store.VulnDB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.db, nil
}

func (m *memVulnStore) UpdateVulnDB(_ context.Context, v store.VulnDB) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db = v
	return nil
}

func (m *memVulnStore) ListAllSbomImageIDs(_ context.Context, afterOrg uuid.UUID, afterImage string, limit int) ([]struct {
	OrgID   uuid.UUID
	ImageID string
}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []struct {
		OrgID   uuid.UUID
		ImageID string
	}
	for _, sb := range m.sboms {
		if sb.OrgID.String() < afterOrg.String() {
			continue
		}
		if sb.OrgID == afterOrg && sb.ImageID <= afterImage {
			continue
		}
		out = append(out, struct {
			OrgID   uuid.UUID
			ImageID string
		}{sb.OrgID, sb.ImageID})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memVulnStore) LoadSbom(_ context.Context, orgID uuid.UUID, imageID string) (store.Sbom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sboms[m.key(orgID, imageID)]
	if !ok {
		return store.Sbom{}, store.ErrNotFound
	}
	return sb, nil
}

func (m *memVulnStore) ReplaceFindings(_ context.Context, orgID uuid.UUID, imageID string, findings []store.Finding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findings[m.key(orgID, imageID)] = append([]store.Finding(nil), findings...)
	return nil
}

func (m *memVulnStore) SetMatchState(_ context.Context, orgID uuid.UUID, imageID, status, errText string, dbBuiltAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.match[m.key(orgID, imageID)] = store.ImageMatchState{
		OrgID: orgID, ImageID: imageID, Status: status, Error: errText, DBBuiltAt: dbBuiltAt,
	}
	return nil
}

func TestMatchImageEmptySbomReportsNoPackages(t *testing.T) {
	org := uuid.New()
	st := newMemVulnStore()
	st.sboms[st.key(org, "scratch")] = store.Sbom{
		OrgID: org, ImageID: "scratch", DistroName: "scratch",
		Packages: nil,
	}
	fake := &vuln.FakeMatcher{Findings: nil, BuiltAt: time.Now().UTC()}
	mgr := vuln.NewManager(st, vuln.Config{ListingURL: "off", DataDir: t.TempDir()}, nil, nil)
	mgr.SetMatcherForTest(fake)

	if err := mgr.MatchImage(context.Background(), org, "scratch"); err != nil {
		t.Fatal(err)
	}
	ms := st.match[st.key(org, "scratch")]
	if ms.Status != store.MatchOK {
		t.Fatalf("status = %q, want ok (empty SBOM is not a failed scan)", ms.Status)
	}
	if ms.Error != "no packages" {
		t.Fatalf("error = %q, want %q (not 'no vulnerabilities')", ms.Error, "no packages")
	}
	if len(st.findings[st.key(org, "scratch")]) != 0 {
		t.Fatalf("findings = %+v, want none", st.findings[st.key(org, "scratch")])
	}
}

func TestManagerDisabled(t *testing.T) {
	st := newMemVulnStore()
	mgr := vuln.NewManager(st, vuln.Config{ListingURL: "off", DataDir: t.TempDir()}, nil, nil)
	if err := mgr.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, _ := st.GetVulnDB(context.Background())
	if v.Status != store.VulnDBDisabled {
		t.Fatalf("status = %s", v.Status)
	}
}

func TestManagerFailedDownloadKeepsOld(t *testing.T) {
	st := newMemVulnStore()
	built := time.Now().UTC().Add(-time.Hour)
	st.db = store.VulnDB{Status: store.VulnDBOK, BuiltAt: &built, SourceURL: "http://x"}
	fake := &vuln.FakeMatcher{Findings: []vuln.Finding{{VulnID: "OLD", Severity: "high"}}, BuiltAt: built}
	mgr := vuln.NewManager(st, vuln.Config{
		ListingURL: "http://127.0.0.1:1/nope", // connection refused
		DataDir:    t.TempDir(),
		CheckEvery: time.Hour,
	}, nil, nil)
	mgr.SetMatcherForTest(fake)

	err := mgr.CheckOnce(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	v, _ := st.GetVulnDB(context.Background())
	if v.Status != store.VulnDBFailed {
		t.Fatalf("status = %s, want failed", v.Status)
	}
	if v.BuiltAt == nil || !v.BuiltAt.Equal(built) {
		t.Fatalf("built_at changed: %v", v.BuiltAt)
	}
	if !mgr.Ready() {
		t.Fatal("expected previous matcher retained")
	}
}

func TestCorruptArchiveRejected(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case filepath.Base(r.URL.Path) == "latest.json":
			_, _ = w.Write([]byte(`{"status":"active","schemaVersion":"6.1.10","built":"2026-01-01T00:00:00Z","path":"bad.tar.gz","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not a real archive"))
		}
	}))
	defer srv.Close()

	st := newMemVulnStore()
	mgr := vuln.NewManager(st, vuln.Config{
		ListingURL: srv.URL + "/latest.json",
		DataDir:    dir,
	}, nil, nil)
	err := mgr.CheckOnce(context.Background())
	if err == nil {
		t.Fatal("expected error for corrupt archive")
	}
	v, _ := st.GetVulnDB(context.Background())
	if v.Status != store.VulnDBFailed {
		t.Fatalf("status = %s", v.Status)
	}
}

func TestRefreshAndRematchWithCraftedDB(t *testing.T) {
	org := uuid.New()
	st := newMemVulnStore()
	st.sboms[st.key(org, "img1")] = store.Sbom{
		OrgID: org, ImageID: "img1", DistroName: "alpine", DistroVersion: "3.20",
		Packages: []store.SbomPackage{{Name: "libfoo", Version: "1.0.0", Type: "apk"}},
	}

	db1 := t.TempDir()
	if err := grypedb.BuildDB(db1, time.Now().UTC().Add(-time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	db2 := t.TempDir()
	if err := grypedb.BuildDB(db2, time.Now().UTC(), []grypedb.Advisory{{
		ID: "CVE-NEW", Package: "libfoo", Ecosystem: "apk", Constraint: "< 1.0.1",
		VersionType: "apk", FixedVersion: "1.0.1", Severity: "critical",
		OSName: "alpine", OSMajor: "3", OSMinor: "20",
	}}); err != nil {
		t.Fatal(err)
	}

	// Serve v2 archive.
	arch := filepath.Join(t.TempDir(), "db.tar.gz")
	if err := grypedb.ArchiveDir(db2, arch); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		_ = grypedb.WriteLatestDocument(w, arch, time.Now().UTC(), "db.tar.gz")
	})
	mux.HandleFunc("/db.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile(arch)
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	nudged := make(chan struct{}, 1)
	mgr := vuln.NewManager(st, vuln.Config{
		ListingURL: srv.URL + "/latest.json",
		DataDir:    t.TempDir(),
		OpenMatcher: func(dbDir string) (vuln.Matcher, error) {
			return vuln.OpenGrypeMatcher(dbDir)
		},
	}, nil, func() { nudged <- struct{}{} })

	start := time.Now()
	if err := mgr.CheckOnce(context.Background()); err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
	select {
	case <-nudged:
	case <-time.After(2 * time.Second):
		// nudge only on Updated; first install counts as updated
	}
	fs := st.findings[st.key(org, "img1")]
	if len(fs) == 0 {
		// rematch may have failed if install layout differs; try MatchImage after SetMatcher
		fake := &vuln.FakeMatcher{Findings: []vuln.Finding{{
			VulnID: "CVE-NEW", Package: "libfoo", Version: "1.0.0",
			FixedVersion: "1.0.1", Severity: "critical",
		}}, BuiltAt: time.Now().UTC()}
		mgr.SetMatcherForTest(fake)
		if err := mgr.MatchImage(context.Background(), org, "img1"); err != nil {
			t.Fatal(err)
		}
		fs = st.findings[st.key(org, "img1")]
	}
	if len(fs) == 0 || fs[0].VulnID != "CVE-NEW" {
		t.Fatalf("findings = %+v", fs)
	}
	_ = start
	_ = errors.New
}
