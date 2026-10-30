// Package web serves strata's web UI: a few static files embedded in the
// binary that read the JSON API. There is nothing to build or deploy
// beyond strata itself.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets
var assets embed.FS

// Handler serves the UI. Unknown paths get the app page, which routes on
// the URL fragment.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // the embedded tree is fixed at build time
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r, sub)
			return
		}
		if _, err := fs.Stat(sub, name); err != nil {
			serveIndex(w, r, sub)
			return
		}
		if strings.HasPrefix(name, "fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// serveIndex writes the app page without caching, so a new release is
// picked up on reload.
func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	page, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(page)
}
