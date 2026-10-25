package grypedb_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

func TestMatcherKnownVulnerablePackage(t *testing.T) {
	dir := t.TempDir()
	built := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	err := grypedb.BuildDB(dir, built, []grypedb.Advisory{{
		ID: "CVE-2024-LIBFOO", Package: "libfoo", Ecosystem: "apk",
		Constraint: "< 1.0.1", VersionType: "apk", FixedVersion: "1.0.1",
		Severity: "critical", Description: "test vuln",
		OSName: "alpine", OSMajor: "3", OSMinor: "20",
	}})
	if err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	m, err := grypedb.OpenMatcher(dir)
	if err != nil {
		t.Fatalf("OpenMatcher: %v", err)
	}
	defer m.Close()

	findings, gotBuilt, err := m.Match(context.Background(), grypedb.Sbom{
		ImageID: "img", DistroName: "alpine", DistroVersion: "3.20",
		Packages: []grypedb.Package{{Name: "libfoo", Version: "1.0.0", Type: "apk", PURL: "pkg:apk/alpine/libfoo@1.0.0"}},
	})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	var hit *grypedb.Finding
	for i := range findings {
		if findings[i].VulnID == "CVE-2024-LIBFOO" {
			hit = &findings[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("CVE-2024-LIBFOO not in %#v", findings)
	}
	if hit.FixedVersion != "1.0.1" {
		t.Errorf("fixed = %q, want 1.0.1", hit.FixedVersion)
	}
	_ = gotBuilt
}
