//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

// inventoryDocument mirrors the helper JSON (subset).
type inventoryDocument struct {
	ImageID string `json:"imageId"`
	Distro  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		IDLike  string `json:"idLike"`
	} `json:"distro"`
	Packages []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Type    string `json:"type"`
		PURL    string `json:"purl"`
	} `json:"packages"`
	PeakRSSBytes int64 `json:"peakRssBytes"`
}

type findingOut struct {
	VulnID       string `json:"vulnId"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	Severity     string `json:"severity"`
	FixedVersion string `json:"fixedVersion,omitempty"`
}

// TestMatchSbom loads an SBOM produced by haetae-inventory and matches it
// with internal/vuln/grypedb against a real Grype v6 database.
func TestMatchSbom(t *testing.T) {
	sbomPath := os.Getenv("HAETAE_BENCH_SBOM")
	dbDir := os.Getenv("HAETAE_BENCH_ACTIVE_DB")
	outPath := os.Getenv("HAETAE_BENCH_FINDINGS_OUT")
	if sbomPath == "" || dbDir == "" || outPath == "" {
		t.Skip("set HAETAE_BENCH_SBOM, HAETAE_BENCH_ACTIVE_DB, HAETAE_BENCH_FINDINGS_OUT")
	}
	raw, err := os.ReadFile(sbomPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc inventoryDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	m, err := grypedb.OpenMatcher(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sbom := grypedb.Sbom{
		ImageID:       doc.ImageID,
		DistroName:    doc.Distro.Name,
		DistroVersion: doc.Distro.Version,
		DistroIDLike:  doc.Distro.IDLike,
	}
	for _, p := range doc.Packages {
		sbom.Packages = append(sbom.Packages, grypedb.Package{
			Name: p.Name, Version: p.Version, Type: p.Type, PURL: p.PURL,
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	findings, built, err := m.Match(ctx, sbom)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]findingOut, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingOut{
			VulnID:       f.VulnID,
			Package:      f.Package,
			Version:      f.Version,
			Severity:     strings.ToLower(f.Severity),
			FixedVersion: f.FixedVersion,
		})
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"imageId":      doc.ImageID,
		"distro":       doc.Distro,
		"packageCount": len(doc.Packages),
		"dbBuiltAt":    built.UTC().Format(time.RFC3339),
		"findingCount": len(out),
		"findings":     out,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURE grype match packages=%d findings=%d db=%s -> %s",
		len(doc.Packages), len(out), built.Format(time.RFC3339), outPath)
}
