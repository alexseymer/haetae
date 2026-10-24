package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestSPAFallbackAndAssets(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":         {Data: []byte("<html>app</html>")},
		"assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"assets/app-abc.css": {Data: []byte("body{}")},
	})

	if r := serve(h, "GET", "/"); r.Code != 200 || !strings.Contains(r.Body.String(), "app") || r.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("/: %d %q cache=%q", r.Code, r.Body.String(), r.Header().Get("Cache-Control"))
	}
	// A client-side route reloads into the app.
	if r := serve(h, "GET", "/hosts/123"); r.Code != 200 || !strings.Contains(r.Body.String(), "app") {
		t.Errorf("/hosts/123: %d %q", r.Code, r.Body.String())
	}
	r := serve(h, "GET", "/assets/app-abc.js")
	if r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") || r.Body.String() != "console.log(1)" {
		t.Errorf("asset: %d %q cache=%q", r.Code, r.Body.String(), r.Header().Get("Cache-Control"))
	}
	// A missing file is a real 404, not the app (it would hide broken asset links).
	if r := serve(h, "GET", "/assets/missing.js"); r.Code != 404 {
		t.Errorf("missing asset: %d", r.Code)
	}
	// Traversal cannot leave the root.
	if r := serve(h, "GET", "/../../etc/passwd"); strings.Contains(r.Body.String(), "root:") {
		t.Error("path traversal")
	}
	if r := serve(h, "POST", "/"); r.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", r.Code)
	}
	csp := serve(h, "GET", "/").Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "script-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows unsafe scripts: %s", csp)
	}
}

func TestEmbeddedPlaceholderServes(t *testing.T) {
	if r := serve(Handler(), "GET", "/"); r.Code != 200 || !strings.Contains(r.Body.String(), "Haetae") {
		t.Fatalf("embedded interface: %d %q", r.Code, r.Body.String())
	}
}
