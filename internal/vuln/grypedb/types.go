package grypedb

import "time"

// Package is one inventory package for matching.
type Package struct {
	Name    string
	Version string
	Type    string
	PURL    string
}

// Sbom is the compact inventory the matcher consumes.
type Sbom struct {
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

// BuiltAt is returned alongside findings (alias for clarity at call sites).
type BuiltAt = time.Time
