package installer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestScriptServedUnchanged(t *testing.T) {
	h := New("")
	r := serve(h, "GET", "/install.sh")
	if r.Code != 200 {
		t.Fatalf("status = %d", r.Code)
	}
	if r.Header().Get("Content-Type") != "text/x-shellscript; charset=utf-8" {
		t.Errorf("Content-Type = %q", r.Header().Get("Content-Type"))
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", r.Header().Get("Cache-Control"))
	}
	if body := r.Body.Bytes(); string(body) != string(script) {
		t.Errorf("body differs from embedded script (%d vs %d bytes)", len(body), len(script))
	}
}

func TestScriptHEAD(t *testing.T) {
	h := New("")
	r := serve(h, "HEAD", "/install.sh")
	if r.Code != 200 {
		t.Fatalf("status = %d", r.Code)
	}
	if r.Body.Len() != 0 {
		t.Errorf("HEAD body = %d bytes", r.Body.Len())
	}
	if r.Header().Get("Content-Type") != "text/x-shellscript; charset=utf-8" {
		t.Errorf("Content-Type = %q", r.Header().Get("Content-Type"))
	}
}

func TestMethodsNotAllowed(t *testing.T) {
	h := New(t.TempDir())
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		for _, path := range []string{"/install.sh", "/download/SHA256SUMS"} {
			r := serve(h, method, path)
			if r.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d", method, path, r.Code)
			}
		}
	}
}

func TestDownloadAllowlistAndTraversal(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "haetae-agent-linux-amd64"), []byte("amd64-bin"))
	mustWrite(t, filepath.Join(dir, "haetae-agent-linux-arm64"), []byte("arm64-bin"))
	mustWrite(t, filepath.Join(dir, "SHA256SUMS"), []byte("sums\n"))
	mustWrite(t, filepath.Join(dir, "evil"), []byte("nope"))
	h := New(dir)

	for _, name := range []string{"haetae-agent-linux-amd64", "haetae-agent-linux-arm64", "SHA256SUMS"} {
		r := serve(h, "GET", "/download/"+name)
		if r.Code != 200 {
			t.Fatalf("%s: %d", name, r.Code)
		}
		if r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", name)
		}
		if r.Header().Get("Content-Disposition") != `attachment; filename="`+name+`"` {
			t.Errorf("%s: Content-Disposition = %q", name, r.Header().Get("Content-Disposition"))
		}
	}
	if got := serve(h, "GET", "/download/haetae-agent-linux-amd64").Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("binary Content-Type = %q", got)
	}
	if got := serve(h, "GET", "/download/SHA256SUMS").Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("SHA256SUMS Content-Type = %q", got)
	}
	if r := serve(h, "HEAD", "/download/SHA256SUMS"); r.Code != 200 || r.Body.Len() != 0 {
		t.Errorf("HEAD download: %d body=%d", r.Code, r.Body.Len())
	}

	// Not on the allowlist, even if the file exists on disk.
	if r := serve(h, "GET", "/download/evil"); r.Code != 404 {
		t.Errorf("evil: %d", r.Code)
	}

	for _, path := range []string{
		"/download/../SHA256SUMS",
		"/download/..%2fSHA256SUMS",
		"/download/%2e%2e/SHA256SUMS",
		"/download/%2e%2e%2fSHA256SUMS",
		"/download/haetae-agent-linux-amd64%2f..",
		"/download/SHA256SUMS%00",
		`/download\SHA256SUMS`,
		"/download//SHA256SUMS",
		"/download/./SHA256SUMS",
		"/download/other",
		"/download/",
		"/download",
	} {
		r := serve(h, "GET", path)
		if r.Code != 404 {
			t.Errorf("%s: got %d, want 404", path, r.Code)
		}
		if body, _ := io.ReadAll(r.Body); string(body) == "sums\n" || string(body) == "amd64-bin" {
			t.Errorf("%s: leaked file contents", path)
		}
	}
}

func TestMissingFileAndEmptyDist(t *testing.T) {
	dir := t.TempDir()
	h := New(dir)
	if r := serve(h, "GET", "/download/haetae-agent-linux-amd64"); r.Code != 404 {
		t.Errorf("missing file: %d", r.Code)
	}
	if r := serve(h, "GET", "/install.sh"); r.Code != 200 {
		t.Errorf("script with empty files still works: %d", r.Code)
	}

	empty := New("")
	if r := serve(empty, "GET", "/download/SHA256SUMS"); r.Code != 404 {
		t.Errorf("empty distDir download: %d", r.Code)
	}
	if r := serve(empty, "GET", "/install.sh"); r.Code != 200 || r.Body.String() != string(script) {
		t.Errorf("empty distDir script: %d", r.Code)
	}
}

func TestMissingDistDir(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if r := serve(h, "GET", "/download/SHA256SUMS"); r.Code != 404 {
		t.Errorf("missing dist dir: %d", r.Code)
	}
	if r := serve(h, "GET", "/install.sh"); r.Code != 200 {
		t.Errorf("script still served: %d", r.Code)
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
