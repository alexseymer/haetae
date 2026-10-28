//go:build screenshots

package screenshots_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

type inventoryDoc struct {
	ImageID string `json:"imageId"`
	Distro  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"distro"`
	Packages []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Type    string `json:"type"`
	} `json:"packages"`
}

func runInventory(t *testing.T, bin, image string) inventoryDoc {
	t.Helper()
	cmd := exec.Command(bin, "--docker-image", image, "--out", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inventory %s: %v\n%s", image, err, out)
	}
	// Helper may write progress on stderr mixed; find JSON object.
	raw := out
	if i := bytes.IndexByte(out, '{'); i >= 0 {
		raw = out[i:]
	}
	var doc inventoryDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse inventory JSON: %v\n%s", err, out)
	}
	if len(doc.Packages) == 0 {
		t.Fatalf("inventory of %s returned no packages", image)
	}
	return doc
}
