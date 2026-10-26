package grypedb

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	v6 "github.com/anchore/grype/grype/db/v6"
	_ "github.com/glebarez/go-sqlite" // registers the "sqlite" driver Grype already links
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
//
// The writer stamps the database with the wall clock; the stamp is rewritten
// to built afterwards so it matches the listing, as it does for real databases.
// Otherwise the update check keeps seeing a "newer" listing and reinstalls.
func BuildDB(dir string, built time.Time, advisories []Advisory) (err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	w, err := v6.NewWriter(v6.Config{DBDirPath: dir})
	if err != nil {
		return fmt.Errorf("grypedb: new writer: %w", err)
	}
	defer func() {
		if cerr := w.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("grypedb: close writer: %w", cerr)
		}
		if err == nil {
			err = stampBuilt(dir, built)
		}
	}()

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
func ArchiveDir(dbDir, archivePath string) (err error) {
	dbDir = filepath.Clean(dbDir)
	archivePath = filepath.Clean(archivePath)
	if !filepath.IsAbs(dbDir) {
		if abs, aerr := filepath.Abs(dbDir); aerr == nil {
			dbDir = abs
		}
	}
	if !filepath.IsAbs(archivePath) {
		if abs, aerr := filepath.Abs(archivePath); aerr == nil {
			archivePath = abs
		}
	}

	f, err := os.Create(archivePath) //nolint:gosec // G304: path is cleaned/abs'd; callers are tests writing a temp archive
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	gz := gzip.NewWriter(f)
	defer func() {
		if cerr := gz.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	tw := tar.NewWriter(gz)
	defer func() {
		if cerr := tw.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

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
		// path comes from Walk under cleaned dbDir (test fixture only).
		rf, err := os.Open(path) //nolint:gosec // G304: Walk path under cleaned dbDir
		if err != nil {
			return err
		}
		defer func() { _ = rf.Close() }()
		_, err = io.Copy(tw, rf)
		return err
	})
}

func ptrTime(t time.Time) *time.Time { return &t }

func stampBuilt(dir string, built time.Time) error {
	db, err := sql.Open("sqlite", filepath.Join(dir, v6.VulnerabilityDBFileName))
	if err != nil {
		return fmt.Errorf("grypedb: open for stamp: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), "UPDATE db_metadata SET build_timestamp = ?", built.UTC().Round(time.Second)); err != nil {
		return fmt.Errorf("grypedb: stamp build time: %w", err)
	}
	return nil
}
