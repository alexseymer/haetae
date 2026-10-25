//go:build deploy || e2e_deploy || e2e_alerts || e2e_security

// Package dockerclean removes leftover Docker resources from deploy integration
// tests and fails the package if any remain after TestMain.
package dockerclean

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const timeout = 2 * time.Minute

// Run wraps m.Run, lists resources whose names start with any prefix, removes
// them, and returns exit code 1 if any existed (a test leaked them).
func Run(m *testing.M, prefixes ...string) int {
	code := m.Run()
	left := List(prefixes...)
	_ = ForceRemove(prefixes...)
	if len(left) > 0 {
		fmt.Fprintf(os.Stderr, "dockerclean: leftovers after tests:\n")
		for _, n := range left {
			fmt.Fprintf(os.Stderr, "  %s\n", n)
		}
		return 1
	}
	return code
}

// ForceRemove deletes containers, networks, and volumes matching prefixes.
// It always uses an independent timeout context so a cancelled test context
// cannot skip cleanup.
func ForceRemove(prefixes ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var first error
	setErr := func(err error) {
		if first == nil && err != nil {
			first = err
		}
	}

	for _, name := range listContainers(ctx, prefixes) {
		setErr(docker(ctx, "rm", "-f", name))
	}
	for _, name := range listNetworks(ctx, prefixes) {
		setErr(docker(ctx, "network", "rm", name))
	}
	for _, name := range listVolumes(ctx, prefixes) {
		setErr(docker(ctx, "volume", "rm", "-f", name))
	}
	for _, ref := range listImages(ctx, prefixes) {
		setErr(docker(ctx, "image", "rm", "-f", ref))
	}
	return first
}

// List returns names of containers, networks, volumes, and images matching prefixes.
func List(prefixes ...string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out []string
	out = append(out, listContainers(ctx, prefixes)...)
	out = append(out, listNetworks(ctx, prefixes)...)
	out = append(out, listVolumes(ctx, prefixes)...)
	out = append(out, listImages(ctx, prefixes)...)
	return out
}

func docker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v: %w\n%s", args, err, out)
	}
	return nil
}

func listContainers(ctx context.Context, prefixes []string) []string {
	return filterNames(dockerLines(ctx, "ps", "-a", "--format", "{{.Names}}"), prefixes)
}

func listNetworks(ctx context.Context, prefixes []string) []string {
	return filterNames(dockerLines(ctx, "network", "ls", "--format", "{{.Name}}"), prefixes)
}

func listVolumes(ctx context.Context, prefixes []string) []string {
	return filterNames(dockerLines(ctx, "volume", "ls", "--format", "{{.Name}}"), prefixes)
}

func listImages(ctx context.Context, prefixes []string) []string {
	// repository:tag lines; also match repository-only when tag is empty.
	return filterNames(dockerLines(ctx, "images", "--format", "{{.Repository}}:{{.Tag}}"), prefixes)
}

func dockerLines(ctx context.Context, args ...string) []string {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func filterNames(names []string, prefixes []string) []string {
	var out []string
	for _, n := range names {
		for _, p := range prefixes {
			if strings.HasPrefix(n, p) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}
