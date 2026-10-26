// Package syftsource is the only place Syft is imported. The resident agent
// must never link this package (see the agent dependency-graph test).
package syftsource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anchore/stereoscope/pkg/image"
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
		// Loopback registries (Docker insecure 127.0.0.0/8) speak HTTP.
		if registryIsLoopback(input) {
			srcCfg = srcCfg.WithRegistryOptions(&image.RegistryOptions{InsecureUseHTTP: true})
		}
	case SourceDir:
		srcCfg = srcCfg.WithSources("dir")
	default:
		return nil, fmt.Errorf("%w: unknown source kind", ErrUnsupported)
	}

	src, err := syft.GetSource(ctx, input, srcCfg)
	if err != nil {
		return nil, classifySourceErr(err)
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
		return nil, fmt.Errorf("catalog: %w", err)
	}
	res := fromSBOM(doc, src)
	if err := checkOSPackages(res); err != nil {
		return nil, err
	}
	return res, nil
}

// classifySourceErr marks only "no recognised source" as ErrUnsupported.
// Docker socket failures, missing images, timeouts etc. stay generic failures.
func classifySourceErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("resolve source: %w", err)
	}
	if strings.Contains(err.Error(), "no source providers were able to resolve") {
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	return fmt.Errorf("resolve source: %w", err)
}

// osPkgType maps a distro id (and id_like) to its package type, "" if unknown.
func osPkgType(ids ...string) string {
	for _, id := range ids {
		switch strings.ToLower(id) {
		case "debian", "ubuntu", "raspbian", "kali":
			return "deb"
		case "alpine", "wolfi", "chainguard":
			return "apk"
		case "rhel", "centos", "fedora", "rocky", "almalinux", "amzn", "ol",
			"opensuse", "suse", "sles", "opensuse-leap", "photon", "mariner", "azurelinux":
			return "rpm"
		}
	}
	return ""
}

// checkOSPackages fails a scan that detected a distro but found none of that
// distro's OS packages (e.g. unreadable package database). No distro
// (distroless/scratch) is fine.
func checkOSPackages(r *Result) error {
	if r.DistroName == "" {
		return nil
	}
	want := osPkgType(append([]string{r.DistroName}, strings.Fields(r.DistroLike)...)...)
	if want == "" {
		return nil
	}
	for _, p := range r.Packages {
		if p.Type == want {
			return nil
		}
	}
	return fmt.Errorf("no OS packages found for %s", r.DistroName)
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

// registryIsLoopback reports whether ref points at localhost / 127.0.0.1 / ::1
// (with or without a port), so InsecureUseHTTP can be enabled for local registries.
func registryIsLoopback(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	// Strip digest / tag noise: host is before the first '/'.
	host := ref
	if i := strings.Index(ref, "/"); i >= 0 {
		host = ref[:i]
	}
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host, "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
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
