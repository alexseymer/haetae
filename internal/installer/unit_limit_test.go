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
	helperCapMB := defaultHelperCapMB(t)
	const minHeadroomMB = 768
	if n < helperCapMB+minHeadroomMB {
		t.Fatalf("MemoryMax=%dM leaves less than %dM above the helper cap (%dM)", n, minHeadroomMB, helperCapMB)
	}
}

// defaultHelperCapMB reads the helper's default cap from the launcher, so the
// test follows the real value instead of a copy of it.
func defaultHelperCapMB(t *testing.T) int {
	t.Helper()
	src, err := os.ReadFile("../agent/scan/launcher.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`defaultMemBytes\s*=\s*(\d+)\s*<<\s*20`).FindSubmatch(src)
	if m == nil {
		t.Fatal("defaultMemBytes in launcher.go is not of the form N << 20")
	}
	mb, _ := strconv.Atoi(string(m[1]))
	return mb
}
