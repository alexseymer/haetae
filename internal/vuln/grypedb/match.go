// Package grypedb isolates all Grype library imports so the rest of the
// control plane compiles without pulling Grype's dependency graph into every
// package that only needs the vuln interfaces.
package grypedb

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anchore/grype/grype"
	v6 "github.com/anchore/grype/grype/db/v6"
	"github.com/anchore/grype/grype/distro"
	"github.com/anchore/grype/grype/matcher"
	grypePkg "github.com/anchore/grype/grype/pkg"
	"github.com/anchore/grype/grype/vulnerability"
	syftPkg "github.com/anchore/syft/syft/pkg"
)

// Matcher matches SBOMs using a Grype v6 database directory (contains vulnerability.db).
type Matcher struct {
	mu     sync.RWMutex
	dbDir  string
	built  time.Time
	prov   vulnerability.Provider
	closed bool
}

// OpenMatcher opens a Grype vulnerability provider from a DB directory that
// already contains vulnerability.db (not the installation root with version subdir).
func OpenMatcher(dbDir string) (*Matcher, error) {
	m := &Matcher{dbDir: dbDir}
	if err := m.reload(); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload reopens the provider after the on-disk DB was replaced.
func (m *Matcher) Reload() error {
	return m.reload()
}

func (m *Matcher) reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("grypedb: matcher closed")
	}
	if m.prov != nil {
		_ = m.prov.Close()
		m.prov = nil
	}
	rdr, err := v6.NewReader(v6.Config{DBDirPath: m.dbDir})
	if err != nil {
		return fmt.Errorf("grypedb: open reader: %w", err)
	}
	desc, err := v6.ReadDescription(v6.Config{DBDirPath: m.dbDir}.DBFilePath())
	built := time.Now().UTC()
	if err == nil && desc != nil && !desc.Built.IsZero() {
		built = desc.Built.Time.UTC()
	}
	m.prov = v6.NewVulnerabilityProvider(rdr)
	m.built = built
	return nil
}

// Close releases the provider.
func (m *Matcher) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.prov != nil {
		err := m.prov.Close()
		m.prov = nil
		return err
	}
	return nil
}

// BuiltAt returns the loaded DB build timestamp.
func (m *Matcher) BuiltAt() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.built
}

// Match runs Grype against the compact SBOM.
func (m *Matcher) Match(ctx context.Context, sbom Sbom) ([]Finding, time.Time, error) {
	m.mu.RLock()
	prov := m.prov
	built := m.built
	m.mu.RUnlock()
	if prov == nil {
		return nil, time.Time{}, fmt.Errorf("grypedb: no vulnerability database loaded")
	}
	select {
	case <-ctx.Done():
		return nil, time.Time{}, ctx.Err()
	default:
	}

	d := distroFromSbom(sbom)
	pkgs := packagesFromSbom(sbom, d)
	vm := &grype.VulnerabilityMatcher{
		VulnerabilityProvider: prov,
		Matchers:              matcher.NewDefaultMatchers(matcher.Config{}),
	}
	matches, _, err := vm.FindMatchesContext(ctx, pkgs, grypePkg.Context{Distro: d})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("grypedb: match: %w", err)
	}

	out := make([]Finding, 0, matches.Count())
	for _, mt := range matches.Sorted() {
		f := Finding{
			VulnID:  mt.Vulnerability.ID,
			Package: mt.Package.Name,
			Version: mt.Package.Version,
			PkgType: string(mt.Package.Type),
		}
		if mt.Vulnerability.Fix.State == vulnerability.FixStateFixed && len(mt.Vulnerability.Fix.Versions) > 0 {
			f.FixedVersion = mt.Vulnerability.Fix.Versions[0]
		}
		if mt.Vulnerability.Metadata != nil {
			f.Severity = strings.ToLower(mt.Vulnerability.Metadata.Severity)
			f.Description = mt.Vulnerability.Metadata.Description
			f.URLs = append(f.URLs, mt.Vulnerability.Metadata.URLs...)
		} else if meta, err := prov.VulnerabilityMetadata(mt.Vulnerability.Reference); err == nil && meta != nil {
			f.Severity = strings.ToLower(meta.Severity)
			f.Description = meta.Description
			f.URLs = append(f.URLs, meta.URLs...)
		}
		if f.Severity == "" {
			f.Severity = "unknown"
		}
		out = append(out, f)
	}
	return out, built, nil
}

func distroFromSbom(sbom Sbom) *distro.Distro {
	name := strings.TrimSpace(strings.ToLower(sbom.DistroName))
	if name == "" {
		return nil
	}
	ver := strings.TrimSpace(sbom.DistroVersion)
	return distro.NewFromNameVersion(name, ver)
}

func packagesFromSbom(sbom Sbom, d *distro.Distro) []grypePkg.Package {
	out := make([]grypePkg.Package, 0, len(sbom.Packages))
	for i, p := range sbom.Packages {
		if p.Name == "" {
			continue
		}
		out = append(out, grypePkg.Package{
			ID:      grypePkg.ID(fmt.Sprintf("%d-%s@%s", i, p.Name, p.Version)),
			Name:    p.Name,
			Version: p.Version,
			Type:    mapPkgType(p.Type, p.PURL),
			PURL:    p.PURL,
			Distro:  d,
		})
	}
	return out
}

func mapPkgType(t, purl string) syftPkg.Type {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "deb", "dpkg", "debian":
		return syftPkg.DebPkg
	case "rpm", "rpmbase":
		return syftPkg.RpmPkg
	case "apk", "alpine":
		return syftPkg.ApkPkg
	case "python", "pypi":
		return syftPkg.PythonPkg
	case "npm", "javascript":
		return syftPkg.NpmPkg
	case "go-module", "go", "golang":
		return syftPkg.GoModulePkg
	case "java", "maven":
		return syftPkg.JavaPkg
	case "gem", "ruby":
		return syftPkg.GemPkg
	case "dotnet", "nuget":
		return syftPkg.DotnetPkg
	}
	switch {
	case strings.HasPrefix(purl, "pkg:deb/"):
		return syftPkg.DebPkg
	case strings.HasPrefix(purl, "pkg:apk/"):
		return syftPkg.ApkPkg
	case strings.HasPrefix(purl, "pkg:rpm/"):
		return syftPkg.RpmPkg
	}
	return syftPkg.UnknownPkg
}
