package frontend

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist/*
var distFS embed.FS

// Handler returns an http.Handler that serves the SPA frontend.
// All non-API, non-file requests fall back to index.html for client-side routing.
func Handler() http.Handler {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("frontend dist not embedded: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't serve frontend for API or Swagger routes.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/swagger/") {
			http.NotFound(w, r)
			return
		}

		// Try to serve the file directly.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Check if file exists in embedded FS.
		if f, err := dist.Open(path); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for all unknown paths.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
