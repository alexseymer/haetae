//go:build screenshots

package screenshots_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// demoLabel marks every container the demo starts, so cleanup finds them even
// though their names (shop-web, worker, ...) look like a real deployment.
const demoLabel = "dev.haetae.screenshots=1"

// TestMain removes demo containers and fails the package if any survived the
// test's own cleanup (same contract as the other harnesses).
func TestMain(m *testing.M) {
	code := m.Run()
	if n := removeDemoContainers(); n > 0 {
		os.Stderr.WriteString("screenshots: removed leftover demo containers\n")
		code = 1
	}
	os.Exit(code)
}

// removeDemoContainers force-removes labelled containers and returns how many existed.
func removeDemoContainers() int {
	out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "label="+demoLabel).Output()
	ids := strings.Fields(string(out))
	if len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
	return len(ids)
}
