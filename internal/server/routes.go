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
	return mux
}
