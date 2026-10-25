// Command haetae-inventory is the short-lived SBOM helper (Syft).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/alexseymer/haetae/internal/inventory"
	"github.com/alexseymer/haetae/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	dockerImage := flag.String("docker-image", "", "image id or ref via the local Docker daemon")
	registry := flag.String("registry", "", "image ref@sha256:... from a registry (no local pull)")
	source := flag.String("source", "", "test-only dir:<path> (requires HAETAE_INVENTORY_TEST=1)")
	dockerSocket := flag.String("docker-socket", "/var/run/docker.sock", "Docker Engine API socket")
	out := flag.String("out", "-", "output path (default stdout)")
	maxPackages := flag.Int("max-packages", inventory.MaxPackages, "fail if more packages are found")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String("haetae-inventory"))
		return
	}

	tempRoot, err := os.MkdirTemp("", "haetae-inventory-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory: temp: %v\n", err)
		os.Exit(inventory.ExitError)
	}
	// Ensure cleanup on SIGTERM as well as normal return (Run also removes).
	cleanup := func() { _ = os.RemoveAll(tempRoot) }
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		cleanup()
	}()

	// Keep Syft scratch under our private temp dir even before Catalog sets TMPDIR.
	_ = os.Setenv("TMPDIR", tempRoot)

	code := inventory.Run(ctx, tempRoot, inventory.Options{
		DockerImage: *dockerImage,
		Registry:    *registry,
		SourceDir:   *source,
		DockerSock:  *dockerSocket,
		Out:         *out,
		MaxPackages: *maxPackages,
	})
	// Run already removed tempRoot; recreate nothing. Absolute path kept for clarity.
	_ = filepath.Clean(tempRoot)
	os.Exit(code)
}
