// Package inventory builds the haetae-inventory JSON document.
package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/inventory/syftsource"
)

// Exit codes for the helper binary.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUnsupported = 2
	ExitTooMany     = 3
)

const (
	// MaxPackages is the hard package cap (also the proto SbomReport limit).
	MaxPackages = 20000
	// MaxOutputBytes caps the JSON document written to --out.
	MaxOutputBytes = 8 << 20
)

// Document is the one JSON object the helper prints.
type Document struct {
	ToolVersion  string `json:"toolVersion"`
	ImageID      string `json:"imageId"`
	Distro       Distro `json:"distro"`
	Packages     []Pkg  `json:"packages"`
	PeakRSSBytes int64  `json:"peakRssBytes"`
	StartedMs    int64  `json:"startedMs"`
	FinishedMs   int64  `json:"finishedMs"`
}

// Distro is the OS release identity.
type Distro struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	IDLike  string `json:"idLike"`
}

// Pkg is one package row.
type Pkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
	PURL    string `json:"purl"`
}

// Options configure one inventory run.
type Options struct {
	DockerImage string // local daemon image id or ref
	Registry    string // ref@sha256:... (no local pull)
	SourceDir   string // test-only: dir path (requires HAETAE_INVENTORY_TEST=1)
	DockerSock  string
	Out         string // empty or "-" = stdout
	MaxPackages int
}

// Run catalogs the target, writes one JSON document, and returns an exit code.
// tempRoot is a private directory that is removed before return.
func Run(ctx context.Context, tempRoot string, opt Options) (exitCode int) {
	started := time.Now()
	if opt.MaxPackages <= 0 {
		opt.MaxPackages = MaxPackages
	}
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "inventory: temp dir: %v\n", err)
		return ExitError
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()

	kind, input, err := resolveSource(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory: %v\n", err)
		return ExitError
	}

	res, err := syftsource.Catalog(ctx, kind, input, opt.DockerSock, tempRoot)
	if err != nil {
		if errors.Is(err, syftsource.ErrUnsupported) {
			fmt.Fprintf(os.Stderr, "inventory: unsupported: %v\n", err)
			return ExitUnsupported
		}
		fmt.Fprintf(os.Stderr, "inventory: %v\n", err)
		return ExitError
	}
	if len(res.Packages) > opt.MaxPackages {
		fmt.Fprintf(os.Stderr, "inventory: %d packages exceeds max %d\n", len(res.Packages), opt.MaxPackages)
		return ExitTooMany
	}

	doc := Document{
		ToolVersion:  res.ToolVersion,
		ImageID:      res.ImageID,
		Distro:       Distro{Name: res.DistroName, Version: res.DistroVer, IDLike: res.DistroLike},
		Packages:     make([]Pkg, 0, len(res.Packages)),
		PeakRSSBytes: PeakRSS(),
		StartedMs:    started.UnixMilli(),
		FinishedMs:   time.Now().UnixMilli(),
	}
	for _, p := range res.Packages {
		doc.Packages = append(doc.Packages, Pkg{Name: p.Name, Version: p.Version, Type: p.Type, PURL: p.PURL})
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory: encode: %v\n", err)
		return ExitError
	}
	if len(raw) > MaxOutputBytes {
		fmt.Fprintf(os.Stderr, "inventory: output %d bytes exceeds %d\n", len(raw), MaxOutputBytes)
		return ExitError
	}
	if err := writeOut(opt.Out, raw); err != nil {
		fmt.Fprintf(os.Stderr, "inventory: write: %v\n", err)
		return ExitError
	}
	return ExitOK
}

func resolveSource(opt Options) (syftsource.SourceKind, string, error) {
	n := 0
	if opt.DockerImage != "" {
		n++
	}
	if opt.Registry != "" {
		n++
	}
	if opt.SourceDir != "" {
		n++
	}
	if n != 1 {
		return 0, "", fmt.Errorf("exactly one of --docker-image, --registry, or --source is required")
	}
	if opt.SourceDir != "" {
		if os.Getenv("HAETAE_INVENTORY_TEST") != "1" {
			return 0, "", fmt.Errorf("--source is only allowed when HAETAE_INVENTORY_TEST=1")
		}
		dir := strings.TrimPrefix(opt.SourceDir, "dir:")
		abs, err := filepath.Abs(dir)
		if err != nil {
			return 0, "", err
		}
		return syftsource.SourceDir, abs, nil
	}
	if opt.Registry != "" {
		return syftsource.SourceRegistry, opt.Registry, nil
	}
	return syftsource.SourceDocker, opt.DockerImage, nil
}

func writeOut(path string, raw []byte) error {
	var w io.Writer = os.Stdout
	if path != "" && path != "-" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // path is CLI --out
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck
		w = f
	}
	if _, err := io.Copy(w, bytes.NewReader(raw)); err != nil {
		return err
	}
	if path == "" || path == "-" {
		_, _ = io.WriteString(os.Stdout, "\n")
	}
	return nil
}
