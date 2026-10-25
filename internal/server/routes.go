package server

import "net/http"

// Routes combines the web server's parts into one handler:
//
//	/api/*, /healthz          the JSON API
//	/install.sh, /download/*  agent installation (installer may be nil)
//	everything else           the web interface
func Routes(api, installer, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/healthz", api)
	if installer != nil {
		mux.Handle("/install.sh", installer)
		mux.Handle("/download/", installer)
	}
	mux.Handle("/", ui)
	return securityHeaders(mux)
}

// securityHeaders sets the headers every response should carry, so none of the
// routes (API, installer, interface) can forget them. The web server is
// HTTPS-only, hence HSTS everywhere.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
