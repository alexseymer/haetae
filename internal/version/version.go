// Package version holds build metadata set by the linker.
package version

import "fmt"

// Set with -ldflags "-X github.com/alexseymer/haetae/internal/version.Version=..." at build time.
var (
	Version = "dev"
	Commit  = "none"
)

// String returns a one-line version description for --version output.
func String(program string) string {
	return fmt.Sprintf("%s %s (%s)", program, Version, Commit)
}
