// Package syftsource is the only place Syft is imported. The resident agent
// must never link this package (see the agent dependency-graph test).
package syftsource

import (
	"context"
	"fmt"
	"strings"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/cataloging"
	"github.com/anchore/syft/syft/sbom"
	"github.com/anchore/syft/syft/source"

	// Registers the SQLite driver used by Syft's RPM DB cataloger.
	_ "modernc.org/sqlite"
)

// ErrUnsupported means Syft could not open or read the image.
var ErrUnsupported = fmt.Errorf("unsupported image")

// Result is a cataloged inventory ready for JSON encoding.
type Result struct {
	ToolVersion string
	ImageID     string
	DistroName  string
	DistroVer   string
	DistroLike  string
	Packages    []Pkg
}

// Pkg is one discovered package.
type Pkg struct {
	Name    string
	Version string
	Type    string
	PURL    string
}

// SourceKind selects how the input is resolved.
type SourceKind int

const (
	SourceDocker SourceKind = iota
	SourceRegistry
	SourceDir // tests only
)

// Catalog runs Syft against input with a tight cataloger set and no file
// cataloging. tempDir is used as TMPDIR for Syft's scratch files.
func Catalog(ctx context.Context, kind SourceKind, input, dockerSocket, tempDir string) (*Result, error) {
	if tempDir != "" {
		prev, had := setEnv("TMPDIR", tempDir)
		defer restoreEnv("TMPDIR", prev, had)
	}
	if dockerSocket != "" && kind == SourceDocker {
		host := dockerSocket
		if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "tcp://") {
			host = "unix://" + host
		}
		prev, had := setEnv("DOCKER_HOST", host)
		defer restoreEnv("DOCKER_HOST", prev, had)
	}

	srcCfg := syft.DefaultGetSourceConfig()
	switch kind {
	case SourceDocker:
		srcCfg = srcCfg.WithSources("docker")
	case SourceRegistry:
		srcCfg = srcCfg.WithSources("registry")
	case SourceDir:
		srcCfg = srcCfg.WithSources("dir")
	default:
		return nil, fmt.Errorf("%w: unknown source kind", ErrUnsupported)
	}

	src, err := syft.GetSource(ctx, input, srcCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	defer func() { _ = src.Close() }()

	cfg := syft.DefaultCreateSBOMConfig().
		WithoutFiles(). // no file/digest cataloging
		WithLicenseConfig(cataloging.LicenseConfig{IncludeContent: cataloging.LicenseContentExcludeAll}).
		WithRelationshipsConfig(cataloging.RelationshipsConfig{}). // skip file-ownership walks
		WithCatalogerSelection(cataloging.NewSelectionRequest().WithDefaults(
			// OS packages + selected language ecosystems only (no secrets/binary/etc.).
			"dpkg", "apk", "rpm",
			"javascript", "python", "go", "java", "ruby", "php", "rust",
		).WithRemovals("file")).
		WithParallelism(1).
		WithTool("haetae-inventory", toolVersion())

	doc, err := syft.CreateSBOM(ctx, src, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	return fromSBOM(doc, src), nil
}

func fromSBOM(doc *sbom.SBOM, src source.Source) *Result {
	r := &Result{ToolVersion: doc.Descriptor.Version}
	if r.ToolVersion == "" {
		r.ToolVersion = toolVersion()
	}
	if meta, ok := src.Describe().Metadata.(source.ImageMetadata); ok {
		r.ImageID = meta.ID
		if r.ImageID == "" {
			r.ImageID = meta.ManifestDigest
		}
	} else if meta, ok := src.Describe().Metadata.(*source.ImageMetadata); ok && meta != nil {
		r.ImageID = meta.ID
		if r.ImageID == "" {
			r.ImageID = meta.ManifestDigest
		}
	}
	if doc.Artifacts.LinuxDistribution != nil {
		d := doc.Artifacts.LinuxDistribution
		r.DistroName = d.ID
		if r.DistroName == "" {
			r.DistroName = d.Name
		}
		r.DistroVer = d.VersionID
		if r.DistroVer == "" {
			r.DistroVer = d.Version
		}
		if len(d.IDLike) > 0 {
			r.DistroLike = strings.Join(d.IDLike, " ")
		}
	}
	for p := range doc.Artifacts.Packages.Enumerate() {
		r.Packages = append(r.Packages, Pkg{
			Name:    p.Name,
			Version: p.Version,
			Type:    string(p.Type),
			PURL:    p.PURL,
		})
	}
	return r
}

func toolVersion() string {
	cfg := syft.DefaultCreateSBOMConfig()
	return cfg.ToolVersion
}

func setEnv(k, v string) (prev string, had bool) {
	prev, had = lookupEnv(k)
	_ = setenv(k, v)
	return prev, had
}

func restoreEnv(k string, prev string, had bool) {
	if !had {
		_ = unsetenv(k)
		return
	}
	_ = setenv(k, prev)
}
