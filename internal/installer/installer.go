// Package installer serves the agent install script and release binaries.
package installer

import (
	"bytes"
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed agent-install.sh
var script []byte

// allowedDownloads is the exact set of names served under /download/.
var allowedDownloads = map[string]string{
	"haetae-agent-linux-amd64": "application/octet-stream",
	"haetae-agent-linux-arm64": "application/octet-stream",
	"SHA256SUMS":               "text/plain",
}

// New returns a handler that serves GET/HEAD /install.sh (the embedded
// script) and GET/HEAD /download/{name} for the allowlisted release files
// in distDir. An empty distDir makes every download a 404; the script still
// works. Other methods get 405.
func New(distDir string) http.Handler {
	return &handler{distDir: distDir}
}

type handler struct {
	distDir string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case r.URL.Path == "/install.sh":
		h.serveScript(w, r)
	case strings.HasPrefix(r.URL.Path, "/download/"):
		h.serveDownload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) serveScript(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/x-shellscript; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "install.sh", time.Time{}, bytes.NewReader(script))
}

func (h *handler) serveDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/download/")
	ctype, ok := allowedDownloads[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Reject percent-encoding, double slashes, backslashes and any other
	// spelling that is not exactly /download/<allowlisted-name>.
	want := "/download/" + name
	if r.URL.EscapedPath() != want {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawPath != "" && r.URL.RawPath != want {
		http.NotFound(w, r)
		return
	}
	if h.distDir == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(h.distDir, name)
	if filepath.Base(path) != name {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is built from the download allowlist, never from user input
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", ctype)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, st.ModTime(), f)
}
