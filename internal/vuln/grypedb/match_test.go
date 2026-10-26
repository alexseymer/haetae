package grypedb_test

import (
	"context"
	"os"
	"path/filepath"
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

func TestOpenMatcherMissingOrCorruptDB(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := grypedb.OpenMatcher(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatal("expected error opening missing DB dir")
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "vulnerability.db"), []byte("not a sqlite db"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := grypedb.OpenMatcher(dir)
		if err == nil {
			t.Fatal("expected error opening corrupt DB")
		}
	})
}

func TestMatchEmptySbom(t *testing.T) {
	dir := t.TempDir()
	if err := grypedb.BuildDB(dir, time.Now().UTC(), nil); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	m, err := grypedb.OpenMatcher(dir)
	if err != nil {
		t.Fatalf("OpenMatcher: %v", err)
	}
	defer m.Close()

	for _, sbom := range []grypedb.Sbom{
		{},
		{ImageID: "empty", Packages: nil},
		{ImageID: "empty", Packages: []grypedb.Package{}},
	} {
		findings, _, err := m.Match(context.Background(), sbom)
		if err != nil {
			t.Fatalf("Match(%+v): %v", sbom, err)
		}
		if len(findings) != 0 {
			t.Fatalf("Match(%+v): findings = %#v, want none (caller reports 'no packages', not a failed scan)", sbom, findings)
		}
	}
}

// Debian advisories are keyed by source package; the binary package only
// links to it through Syft's "upstream" PURL qualifier.
func TestMatchUsesUpstreamSourcePackage(t *testing.T) {
	dir := t.TempDir()
	err := grypedb.BuildDB(dir, time.Now(), []grypedb.Advisory{{
		ID: "CVE-2024-PAM", Package: "pam", Ecosystem: "deb",
		Constraint: "< 1.5.2-7", VersionType: "deb", FixedVersion: "1.5.2-7",
		Severity: "high", OSName: "debian", OSMajor: "12",
	}})
	if err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	m, err := grypedb.OpenMatcher(dir)
	if err != nil {
		t.Fatalf("OpenMatcher: %v", err)
	}
	defer m.Close()

	match := func(purl string) []grypedb.Finding {
		t.Helper()
		f, _, err := m.Match(context.Background(), grypedb.Sbom{
			ImageID: "img", DistroName: "debian", DistroVersion: "12",
			Packages: []grypedb.Package{{Name: "libpam-modules", Version: "1.5.2-6", Type: "deb", PURL: purl}},
		})
		if err != nil {
			t.Fatalf("Match: %v", err)
		}
		return f
	}
	f := match("pkg:deb/debian/libpam-modules@1.5.2-6?arch=amd64&distro=debian-12&upstream=pam")
	if len(f) != 1 || f[0].VulnID != "CVE-2024-PAM" || f[0].Package != "libpam-modules" || f[0].FixedVersion != "1.5.2-7" {
		t.Fatalf("with upstream qualifier: got %#v", f)
	}
	if f := match("pkg:deb/debian/libpam-modules@1.5.2-6?arch=amd64&distro=debian-12"); len(f) != 0 {
		t.Fatalf("without upstream qualifier the binary name must not match: %#v", f)
	}
}
