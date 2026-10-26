package grypedb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

func TestRefreshInstallsNewerBuildOnce(t *testing.T) {
	var mu sync.Mutex
	var archive string
	var built time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = grypedb.WriteLatestDocument(w, archive, built, "db.tar.gz")
	})
	mux.HandleFunc("/db.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		a := archive
		mu.Unlock()
		b, _ := os.ReadFile(a) //nolint:gosec // test fixture
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	work := t.TempDir()
	publish := func(name string, at time.Time, ids ...string) {
		var adv []grypedb.Advisory
		for _, id := range ids {
			adv = append(adv, grypedb.Advisory{
				ID: id, Package: "libfoo", Ecosystem: "apk", Constraint: "< 2",
				VersionType: "apk", FixedVersion: "2", Severity: "high",
				OSName: "alpine", OSMajor: "3", OSMinor: "20",
			})
		}
		dir := filepath.Join(work, name)
		if err := grypedb.BuildDB(dir, at, adv); err != nil {
			t.Fatal(err)
		}
		arch := dir + ".tar.gz"
		if err := grypedb.ArchiveDir(dir, arch); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		archive, built = arch, at
		mu.Unlock()
	}

	data := t.TempDir()
	ctx := context.Background()
	url := srv.URL + "/latest.json"
	match := func() map[string]bool {
		m, err := grypedb.OpenMatcher(grypedb.ActiveDBDir(data))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		fs, _, err := m.Match(ctx, grypedb.Sbom{ImageID: "i", DistroName: "alpine", DistroVersion: "3.20",
			Packages: []grypedb.Package{{Name: "libfoo", Version: "1", Type: "apk", PURL: "pkg:apk/alpine/libfoo@1"}}})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, f := range fs {
			got[f.VulnID] = true
		}
		return got
	}

	publish("a", time.Now().UTC().Add(-time.Minute).Truncate(time.Second), "CVE-A")
	if r := grypedb.Refresh(ctx, data, url); r.Error != nil || !r.Updated {
		t.Fatalf("first refresh: %+v", r)
	}
	if r := grypedb.Refresh(ctx, data, url); r.Error != nil || r.Updated {
		t.Fatalf("second refresh must be a no-op: %+v", r)
	}
	held, err := grypedb.OpenMatcher(grypedb.ActiveDBDir(data))
	if err != nil {
		t.Fatal(err)
	}
	publish("b", time.Now().UTC().Add(2*time.Second).Truncate(time.Second), "CVE-A", "CVE-B")
	if r := grypedb.Refresh(ctx, data, url); r.Error != nil || !r.Updated {
		t.Fatalf("third refresh: %+v", r)
	}
	// The replaced file stays visible to readers opened while the old one is
	// still open, so the old matcher must be closed before the new one opens.
	_ = held.Close()
	if got := match(); !got["CVE-B"] {
		t.Fatalf("new advisory missing after update: %v", got)
	}
	if r := grypedb.Refresh(ctx, data, url); r.Error != nil || r.Updated {
		t.Fatalf("fourth refresh must be a no-op: %+v", r)
	}
}
