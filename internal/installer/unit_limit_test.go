package installer

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The agent's unit must allow the inventory helper's 256 MB cap plus the page
// cache that reading image layers charges to the same cgroup. A 400 MB limit
// let the kernel kill the agent whenever a large image was scanned.
func TestUnitMemoryMaxLeavesHeadroomForHelper(t *testing.T) {
	src, err := os.ReadFile("agent-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^MemoryMax=(\d+)([MG])$`).FindSubmatch(src)
	if m == nil {
		t.Fatal("agent-install.sh has no MemoryMax=<n>M|G line")
	}
	n, _ := strconv.Atoi(string(m[1]))
	if strings.EqualFold(string(m[2]), "G") {
		n *= 1024
	}
	const helperCapMB, minHeadroomMB = 256, 768
	if n < helperCapMB+minHeadroomMB {
		t.Fatalf("MemoryMax=%dM leaves less than %dM above the helper cap", n, minHeadroomMB)
	}
}
