// Package webui serves the single-page web interface embedded in the server.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The interface is built from web/ into app/ by `make web` (app/ is not
// committed). fallback/ holds a placeholder page that is committed, so the
// server always builds and says what is missing when the interface was not
// built into it.
//
//go:embed all:app all:fallback
var embedded embed.FS

// Handler serves the interface: real files as they are, and any other path
// below the root as index.html so client-side routes work on reload.
func Handler() http.Handler {
	dir := "fallback"
	if _, err := fs.Stat(embedded, "app/index.html"); err == nil {
		dir = "app"
	}
	sub, err := fs.Sub(embedded, dir)
	if err != nil {
		panic(err)
	}
	return handler(sub)
}

func handler(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// The interface loads only its own scripts and styles and talks only to
		// this server.
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		if name != "" {
			if f, err := files.Open(name); err == nil {
				st, serr := f.Stat()
				_ = f.Close()
				if serr == nil && !st.IsDir() {
					// Hashed asset files never change; the page itself must always be fetched fresh.
					if strings.HasPrefix(name, "assets/") {
						h.Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					r.URL.Path = clean
					fileServer.ServeHTTP(w, r)
					return
				}
			}
			// A path that looks like a file (has an extension) but is missing is a real 404.
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		h.Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
