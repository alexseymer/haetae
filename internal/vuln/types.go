// Package vuln matches SBOMs against the Grype vulnerability database.
package vuln

import (
	"context"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

// Package is one inventory package (mirrors store.SbomPackage for the matcher).
type Package struct {
	Name    string
	Version string
	Type    string
	PURL    string
}

// Sbom is the compact inventory the matcher consumes.
type Sbom struct {
	OrgID         string
	ImageID       string
	DistroName    string
	DistroVersion string
	DistroIDLike  string
	Packages      []Package
}

// Finding is one vulnerability match result.
type Finding struct {
	VulnID       string
	Package      string
	Version      string
	PkgType      string
	FixedVersion string
	Severity     string
	Description  string
	URLs         []string
}

// Matcher matches an SBOM against a vulnerability database.
type Matcher interface {
	Match(ctx context.Context, sbom Sbom) (findings []Finding, dbBuiltAt time.Time, err error)
}

// FromStoreSbom converts a store SBOM into the matcher input.
func FromStoreSbom(sb store.Sbom) Sbom {
	out := Sbom{
		OrgID:         sb.OrgID.String(),
		ImageID:       sb.ImageID,
		DistroName:    sb.DistroName,
		DistroVersion: sb.DistroVersion,
		DistroIDLike:  sb.DistroIDLike,
	}
	for _, p := range sb.Packages {
		out.Packages = append(out.Packages, Package{
			Name: p.Name, Version: p.Version, Type: p.Type, PURL: p.PURL,
		})
	}
	return out
}

// ToStoreFindings converts matcher findings for ReplaceFindings.
func ToStoreFindings(in []Finding) []store.Finding {
	out := make([]store.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, store.Finding{
			VulnID:       f.VulnID,
			Package:      f.Package,
			Version:      f.Version,
			PkgType:      f.PkgType,
			FixedVersion: f.FixedVersion,
			Severity:     store.NormalizeFindingSeverity(f.Severity),
			Description:  f.Description,
			URLs:         f.URLs,
		})
	}
	return out
}
