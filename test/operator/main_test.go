//go:build e2e_operator

package operator_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain fails the package when a test leaked an htest- container, network
// or volume (same contract as test/security, which cannot be imported).
func TestMain(m *testing.M) {
	code := m.Run()
	var left []string
	for _, args := range [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"network", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
	} {
		out, _ := exec.Command("docker", args...).Output()
		for _, n := range strings.Fields(string(out)) {
			if strings.HasPrefix(n, "htest-") {
				left = append(left, n)
			}
		}
	}
	if len(left) > 0 {
		fmt.Fprintf(os.Stderr, "leftovers after tests: %v\n", left)
		for _, n := range left {
			_ = exec.Command("docker", "rm", "-f", n).Run()
			_ = exec.Command("docker", "network", "rm", n).Run()
			_ = exec.Command("docker", "volume", "rm", "-f", n).Run()
		}
		code = 1
	}
	os.Exit(code)
}
