package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

// handleStatic serves embedded UI assets, mirroring the Rust `statics` module.
// On a miss it falls back to index.html (SPA routing), except under /api where a
// miss is a genuine 404 (mirroring warp's NOT_FOUND).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Path relative to the asset root: strip the leading slash. (Base-path
	// prefix, if any, was already removed by http.StripPrefix.)
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

	// Unknown API routes are 404 NOT_FOUND, not SPA fallback.
	if name == "api" || strings.HasPrefix(name, "api/") {
		http.Error(w, "NOT_FOUND", http.StatusNotFound)
		return
	}

	if name == "" {
		s.serveIndex(w)
		return
	}

	data, err := fs.ReadFile(s.ui, name)
	if err != nil {
		// SPA fallback (Rust's homepage filter serves index.html for GET).
		s.serveIndex(w)
		return
	}

	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Cache-Control", "max-age=3600, must-revalidate")

	// For UTF-8 text assets, substitute the assets path placeholder, as Rust does
	// for any utf8-decodable file.
	if utf8.Valid(data) {
		data = []byte(strings.ReplaceAll(string(data), "/__ASSETS_PATH__", s.assetsReplacer))
	}
	_, _ = w.Write(data)
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write(s.indexHTML)
}

// contentType maps a file extension to a content type, matching the Rust match.
func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".css"):
		return "text/css"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript"
	case strings.HasSuffix(name, ".html"):
		return "text/html"
	default:
		return "application/octet-stream"
	}
}
