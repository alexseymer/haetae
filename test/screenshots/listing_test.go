//go:build screenshots

package screenshots_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

// listingServer serves a crafted Grype v6 latest.json + archive that can be
// swapped to a newer build for rematch / new_cve scenarios.
type listingServer struct {
	mu      sync.Mutex
	archive string
	built   time.Time
	srv     *httptest.Server
}

func startListingServer(t *testing.T) *listingServer {
	t.Helper()
	ls := &listingServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		arch, built := ls.archive, ls.built
		ls.mu.Unlock()
		if arch == "" {
			http.NotFound(w, r)
			return
		}
		_ = grypedb.WriteLatestDocument(w, arch, built, "db.tar.gz")
	})
	mux.HandleFunc("/db.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		arch := ls.archive
		ls.mu.Unlock()
		if arch == "" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(arch) //nolint:gosec // test fixture path
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(b)
	})
	ls.srv = httptest.NewServer(mux)
	t.Cleanup(ls.srv.Close)
	return ls
}

func (ls *listingServer) URL() string {
	return ls.srv.URL + "/latest.json"
}

func (ls *listingServer) publish(t *testing.T, dir string, built time.Time, advisories []grypedb.Advisory) {
	t.Helper()
	dbDir := filepath.Join(dir, "db-"+built.Format("150405.000"))
	if err := grypedb.BuildDB(dbDir, built, advisories); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	arch := filepath.Join(dir, "db-"+built.Format("150405.000")+".tar.gz")
	if err := grypedb.ArchiveDir(dbDir, arch); err != nil {
		t.Fatalf("ArchiveDir: %v", err)
	}
	ls.mu.Lock()
	ls.archive = arch
	ls.built = built
	ls.mu.Unlock()
}
