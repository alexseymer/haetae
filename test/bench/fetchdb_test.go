//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

const defaultListing = "https://grype.anchore.io/databases/v6/latest.json"

// TestFetchGrypeDB downloads the real Anchore Grype v6 database through
// grypedb.Refresh (same code path as the control-plane manager).
func TestFetchGrypeDB(t *testing.T) {
	dir := os.Getenv("HAETAE_BENCH_DB_DIR")
	if dir == "" {
		t.Skip("set HAETAE_BENCH_DB_DIR to the cache data dir (installation root)")
	}
	listing := os.Getenv("HAETAE_BENCH_LISTING_URL")
	if listing == "" {
		listing = defaultListing
	}
	if err := grypedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	res := grypedb.Refresh(ctx, dir, listing)
	if res.Error != nil {
		t.Fatalf("grypedb.Refresh: %v", res.Error)
	}
	active := grypedb.ActiveDBDir(dir)
	if _, err := os.Stat(filepath.Join(active, "vulnerability.db")); err != nil {
		t.Fatalf("active db missing: %v (dir=%s)", err, active)
	}
	meta := map[string]any{
		"updated":   res.Updated,
		"builtAt":   res.BuiltAt.UTC().Format(time.RFC3339),
		"checkedAt": res.CheckedAt.UTC().Format(time.RFC3339),
		"activeDir": active,
		"listing":   listing,
		"message":   res.Message,
	}
	out := os.Getenv("HAETAE_BENCH_DB_META")
	if out == "" {
		out = filepath.Join(dir, "fetch-meta.json")
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURE grype db active=%s built=%s updated=%v", active, res.BuiltAt, res.Updated)
}
