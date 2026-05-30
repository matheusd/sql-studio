package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

// dbPrefix is the fixed path segment the entire app (UI + JSON API) is served
// under, so the API lives at e.g. "/db/api/tables". It is composed after any
// configured BasePath, and is the subtree a host server mounts Handler() at.
const dbPrefix = "/db"

// Options configures the HTTP server. Mirrors the relevant Rust CLI args.
type Options struct {
	// Address to bind to, e.g. "127.0.0.1:3030".
	Address string
	// BasePath to serve the UI under, e.g. "/sql-studio". Empty for root.
	// Must start with "/" when set.
	BasePath string
	// NoShutdown disables the /api/shutdown endpoint and the UI shutdown button.
	NoShutdown bool
	// Version reported by /api/metadata.
	Version string
}

// Server serves the embedded UI and the JSON API backed by a Database.
type Server struct {
	db   Database
	ui   fs.FS
	opts Options

	// indexHTML is the (prefix-rewritten) index.html served for SPA routes.
	indexHTML []byte
	// assetsReplacer is the string that replaces "/__ASSETS_PATH__" in assets
	// (the prefix the app is served under).
	assetsReplacer string
	// prefix is the path subtree the whole app is served under: BasePath + "/db".
	prefix string

	shutdownCh chan struct{}
}

// New builds a Server. The ui filesystem holds the built UI assets (e.g. an
// embed.FS rooted at the dist directory); the caller owns embedding so this
// package stays free of bundled binaries. New reads and rewrites index.html
// according to opts.BasePath, mirroring the Rust startup logic.
func New(db Database, ui fs.FS, opts Options) (*Server, error) {
	indexBytes, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		return nil, err
	}
	index := string(indexBytes)

	// The app is always served under a "/db" subtree, composed after any
	// configured base path. This prefix is baked into index.html for the UI
	// router and API calls, and is where a host server mounts Handler().
	prefix := opts.BasePath + dbPrefix

	base := `<meta name="BASE_PATH" content="` + prefix + `" />`
	index = strings.Replace(index, `<!-- __BASE__ -->`, base, 1)
	index = strings.ReplaceAll(index, "/__ASSETS_PATH__", prefix)

	return &Server{
		db:             db,
		ui:             ui,
		opts:           opts,
		indexHTML:      []byte(index),
		assetsReplacer: prefix,
		prefix:         prefix,
		shutdownCh:     make(chan struct{}, 1),
	}, nil
}

// Handler returns the fully-wired HTTP handler: the JSON API and the embedded
// UI, all served under the server's prefix (see Prefix). A host application can
// reach into this package as a library and mount the returned handler, e.g.:
//
//	mux.Handle(srv.Prefix()+"/", srv.Handler())
//
// CORS is applied to the app routes; the prefix redirects are not wrapped.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API routes (under /api). Order/semantics mirror Rust handlers::routes.
	mux.HandleFunc("GET /api/{$}", s.handleOverview)
	mux.HandleFunc("GET /api/tables", s.handleTables)
	mux.HandleFunc("GET /api/tables/{name}", s.handleTable)
	mux.HandleFunc("GET /api/tables/{name}/data", s.handleTableData)
	mux.HandleFunc("GET /api/autocomplete", s.handleAutocomplete)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	mux.HandleFunc("GET /api/metadata", s.handleMetadata)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("GET /api/erd", s.handleErd)

	// Everything else: static assets, with index.html as SPA fallback.
	mux.HandleFunc("/", s.handleStatic)

	h := withCORS(mux)

	// Mount the whole app under s.prefix (base path + "/db") and strip it so the
	// inner mux sees plain /api and / paths. Mounting under the prefix here means
	// a host server can route the prefix subtree straight at this handler.
	top := http.NewServeMux()
	top.Handle(s.prefix+"/", http.StripPrefix(s.prefix, h))
	top.HandleFunc(s.prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.prefix+"/", http.StatusMovedPermanently)
	})
	// Standalone convenience: send the bare root into the app subtree. When
	// mounted in a host server the host owns "/" and this is never reached.
	top.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.prefix+"/", http.StatusFound)
	})

	return top
}

// Prefix is the path subtree the app is served under: the configured base path
// followed by "/db". A host server mounts Handler() at Prefix()+"/".
func (s *Server) Prefix() string {
	return s.prefix
}

// Run starts the HTTP server and blocks until the context is cancelled (e.g.
// SIGINT) or a shutdown request arrives on /api/shutdown.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.opts.Address, Handler: s.Handler()}

	go func() {
		select {
		case <-ctx.Done():
		case <-s.shutdownCh:
			slog.Info("received shutdown signal")
		}
		// Use a fresh context so in-flight requests get a chance to drain.
		_ = srv.Shutdown(context.Background())
	}()

	slog.Info("listening", "address", s.opts.Address)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		slog.Info("shutting down...")
		return nil
	}
	return err
}

// withCORS replicates the Rust warp CORS config: any origin; GET/POST/DELETE;
// Content-Length and Content-Type headers.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
		h.Set("Access-Control-Allow-Headers", "Content-Length, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON encodes v as JSON; on encode failure it reports a 500 with the same
// plain-text body Rust uses.
func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		internalError(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf)
}

// internalError mirrors the Rust 500 rejection body.
func internalError(w http.ResponseWriter) {
	http.Error(w, "INTERNAL_SERVER_ERROR", http.StatusInternalServerError)
}
