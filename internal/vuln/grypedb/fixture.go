package grypedb

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	v6 "github.com/anchore/grype/grype/db/v6"
)

// Advisory describes one crafted vulnerability for a test database.
type Advisory struct {
	ID           string
	Package      string
	Ecosystem    string // e.g. "apk", "deb"
	Constraint   string // e.g. "< 1.0.1"
	VersionType  string // e.g. "apk", "deb"
	FixedVersion string
	Severity     string // critical, high, ...
	Description  string
	OSName       string // e.g. "alpine"
	OSMajor      string // e.g. "3"
	OSMinor      string
}

// BuildDB writes a minimal Grype v6 database into dir containing the given advisories.
func BuildDB(dir string, built time.Time, advisories []Advisory) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	w, err := v6.NewWriter(v6.Config{DBDirPath: dir})
	if err != nil {
		return fmt.Errorf("grypedb: new writer: %w", err)
	}
	defer w.Close()

	provider := v6.Provider{
		ID:           "test",
		Version:      "1",
		Processor:    "haetae-test",
		DateCaptured: ptrTime(built),
		InputDigest:  "test",
	}
	if err := w.AddProvider(provider); err != nil {
		return err
	}

	for _, a := range advisories {
		if a.Severity == "" {
			a.Severity = "high"
		}
		if a.VersionType == "" {
			a.VersionType = a.Ecosystem
		}
		prov := provider
		vuln := &v6.VulnerabilityHandle{
			Name:       a.ID,
			ProviderID: provider.ID,
			Provider:   &prov,
			BlobValue: &v6.VulnerabilityBlob{
				ID:          a.ID,
				Description: a.Description,
				Severities: []v6.Severity{{
					Scheme: v6.SeveritySchemeCHMLN,
					Value:  a.Severity,
					Rank:   1,
				}},
			},
		}
		if err := w.AddVulnerabilities(vuln); err != nil {
			return err
		}
		var osRef *v6.OperatingSystem
		if a.OSName != "" {
			osRef = &v6.OperatingSystem{
				Name:         a.OSName,
				MajorVersion: a.OSMajor,
				MinorVersion: a.OSMinor,
			}
		}
		var fix *v6.Fix
		if a.FixedVersion != "" {
			fix = &v6.Fix{Version: a.FixedVersion, State: v6.FixedStatus}
		}
		ap := &v6.AffectedPackageHandle{
			Vulnerability:   vuln,
			OperatingSystem: osRef,
			Package: &v6.Package{
				Ecosystem: a.Ecosystem,
				Name:      a.Package,
			},
			BlobValue: &v6.PackageBlob{
				Ranges: []v6.Range{{
					Fix: fix,
					Version: v6.Version{
						Type:       a.VersionType,
						Constraint: a.Constraint,
					},
				}},
			},
		}
		if err := w.AddAffectedPackages(ap); err != nil {
			return err
		}
	}
	_ = built
	return nil
}

// ArchiveDir tars+gzips the contents of dbDir into archivePath (tar.gz).
func ArchiveDir(dbDir, archivePath string) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(dbDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dbDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rf, err := os.Open(path)
		if err != nil {
			return err
		}
		defer rf.Close()
		_, err = io.Copy(tw, rf)
		return err
	})
}

func ptrTime(t time.Time) *time.Time { return &t }
