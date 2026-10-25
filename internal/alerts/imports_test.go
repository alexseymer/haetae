package alerts_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The alerts package must stay AI-free and must not dial the network.
// Ban common HTTP client packages so a future import fails the build.
func TestAlertsImportsAreLocalOnly(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{
		"net/http",
		"net/http/httptest",
		"golang.org/x/net/http2",
		"github.com/alexseymer/haetae/internal/chat",
		"github.com/alexseymer/haetae/internal/briefing",
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, b := range banned {
				if path == b || strings.HasPrefix(path, b+"/") {
					t.Errorf("%s imports banned package %q", e.Name(), path)
				}
			}
			// Any package with "openai", "anthropic", or "ai/" in the path is banned.
			lp := strings.ToLower(path)
			if strings.Contains(lp, "openai") || strings.Contains(lp, "anthropic") ||
				strings.Contains(lp, "/ai/") || strings.HasSuffix(lp, "/ai") {
				t.Errorf("%s imports AI-related package %q", e.Name(), path)
			}
		}
	}
}
