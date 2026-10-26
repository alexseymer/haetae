package inventory_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexseymer/haetae/internal/inventory"
)

func TestCatalogAPKFixture(t *testing.T) {
	t.Setenv("HAETAE_INVENTORY_TEST", "1")
	dir := filepath.Join("testdata", "apk")
	out := filepath.Join(t.TempDir(), "out.json")
	temp := filepath.Join(t.TempDir(), "work")
	code := inventory.Run(context.Background(), temp, inventory.Options{
		SourceDir: "dir:" + dir,
		Out:       out,
	})
	if code != inventory.ExitOK {
		t.Fatalf("exit %d", code)
	}
	doc := readDoc(t, out)
	if doc.Distro.Name != "alpine" {
		t.Fatalf("distro name = %q", doc.Distro.Name)
	}
	found := map[string]string{}
	for _, p := range doc.Packages {
		found[p.Name] = p.Version
	}
	if found["musl"] == "" || found["busybox"] == "" {
		t.Fatalf("packages = %#v, want musl and busybox", found)
	}
}

func TestCatalogDPKGFixture(t *testing.T) {
	t.Setenv("HAETAE_INVENTORY_TEST", "1")
	dir := filepath.Join("testdata", "dpkg")
	out := filepath.Join(t.TempDir(), "out.json")
	temp := filepath.Join(t.TempDir(), "work")
	code := inventory.Run(context.Background(), temp, inventory.Options{
		SourceDir: "dir:" + dir,
		Out:       out,
	})
	if code != inventory.ExitOK {
		t.Fatalf("exit %d", code)
	}
	doc := readDoc(t, out)
	if doc.Distro.Name != "debian" {
		t.Fatalf("distro name = %q", doc.Distro.Name)
	}
	found := false
	for _, p := range doc.Packages {
		if p.Name == "libc6" && p.Version != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("packages = %#v, want libc6", doc.Packages)
	}
}

func TestSourceRequiresTestEnv(t *testing.T) {
	t.Setenv("HAETAE_INVENTORY_TEST", "")
	code := inventory.Run(context.Background(), t.TempDir(), inventory.Options{
		SourceDir: "dir:" + t.TempDir(),
		Out:       filepath.Join(t.TempDir(), "out.json"),
	})
	if code != inventory.ExitError {
		t.Fatalf("exit %d, want %d", code, inventory.ExitError)
	}
}

func readDoc(t *testing.T, path string) inventory.Document {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	var doc inventory.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	return doc
}
