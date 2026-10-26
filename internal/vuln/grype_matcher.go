package vuln

import (
	"context"
	"time"

	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

// GrypeMatcher adapts grypedb.Matcher to the vuln.Matcher interface.
type GrypeMatcher struct {
	inner *grypedb.Matcher
}

// OpenGrypeMatcher opens a Grype-backed matcher for dbDir.
func OpenGrypeMatcher(dbDir string) (*GrypeMatcher, error) {
	m, err := grypedb.OpenMatcher(dbDir)
	if err != nil {
		return nil, err
	}
	return &GrypeMatcher{inner: m}, nil
}

// Match implements Matcher.
func (g *GrypeMatcher) Match(ctx context.Context, sbom Sbom) ([]Finding, time.Time, error) {
	in := grypedb.Sbom{
		ImageID: sbom.ImageID, DistroName: sbom.DistroName,
		DistroVersion: sbom.DistroVersion, DistroIDLike: sbom.DistroIDLike,
	}
	for _, p := range sbom.Packages {
		in.Packages = append(in.Packages, grypedb.Package{
			Name: p.Name, Version: p.Version, Type: p.Type, PURL: p.PURL,
		})
	}
	raw, built, err := g.inner.Match(ctx, in)
	if err != nil {
		return nil, time.Time{}, err
	}
	out := make([]Finding, 0, len(raw))
	for _, f := range raw {
		out = append(out, Finding{
			VulnID: f.VulnID, Package: f.Package, Version: f.Version, PkgType: f.PkgType,
			FixedVersion: f.FixedVersion, Severity: f.Severity, Description: f.Description, URLs: f.URLs,
		})
	}
	return out, built, nil
}

// Close releases the underlying provider.
func (g *GrypeMatcher) Close() error {
	if g == nil || g.inner == nil {
		return nil
	}
	return g.inner.Close()
}

// BuiltAt returns the DB build time.
func (g *GrypeMatcher) BuiltAt() time.Time {
	if g == nil || g.inner == nil {
		return time.Time{}
	}
	return g.inner.BuiltAt()
}

// Reload reopens after an on-disk swap.
func (g *GrypeMatcher) Reload() error {
	return g.inner.Reload()
}
