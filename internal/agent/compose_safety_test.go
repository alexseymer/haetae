package agent_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Forbidden substrings that must not appear in agent Go sources outside tests
// and comments. Guards volumes-never-removed / no-destructive-shell rules.
var forbiddenAgentSnippets = []string{
	"--volumes",
	" -v",
	"volume rm",
	"system prune",
	"rm -rf",
}

func TestAgentSourcesRefuseDestructiveComposeFlags(t *testing.T) {
	root := "."
	// Prefer package-relative path when tests run from repo root via ./internal/agent.
	if fi, err := os.Stat("internal/agent"); err == nil && fi.IsDir() {
		root = "internal/agent"
	}
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			code := line
			if j := strings.Index(code, "//"); j >= 0 {
				code = code[:j]
			}
			for _, snip := range forbiddenAgentSnippets {
				if strings.Contains(code, snip) {
					offenders = append(offenders, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("forbidden destructive snippets in agent sources:\n%s", strings.Join(offenders, "\n"))
	}
}
