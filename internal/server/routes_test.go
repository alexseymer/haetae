package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEveryRouteCarriesSecurityHeaders(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Routes(ok, ok, ok)
	for _, path := range []string{"/", "/hosts/1", "/api/me", "/healthz", "/install.sh", "/download/haetae-agent-linux-amd64", "/nope"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		for _, hdr := range []string{"Strict-Transport-Security", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
			if rec.Header().Get(hdr) == "" {
				t.Errorf("%s: missing %s", path, hdr)
			}
		}
	}
}
