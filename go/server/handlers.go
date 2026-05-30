package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Overview(r.Context())
	if err != nil {
		slog.Error("error while getting database overview", "err", err)
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}

func (s *Server) handleTables(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Tables(r.Context())
	if err != nil {
		slog.Error("error while getting tables", "err", err)
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}

func (s *Server) handleTable(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, err := s.db.Table(r.Context(), name)
	if err != nil {
		slog.Error("error while getting table", "err", err)
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}

func (s *Server) handleTableData(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			page = v
		}
	}
	res, err := s.db.TableData(r.Context(), name, page)
	if err != nil {
		slog.Error("error while getting table data", "err", err)
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}

func (s *Server) handleAutocomplete(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.TablesWithColumns(r.Context())
	if err != nil {
		slog.Error("error while getting autocomplete data", "err", err)
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Mirrors warp's BodyDeserializeError -> 400 BAD_REQUEST.
		http.Error(w, "BAD_REQUEST", http.StatusBadRequest)
		return
	}
	res, err := s.db.Query(r.Context(), body.Query)
	if err != nil {
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, &Metadata{
		Version:     s.opts.Version,
		CanShutdown: !s.opts.NoShutdown,
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !s.opts.NoShutdown {
		select {
		case s.shutdownCh <- struct{}{}:
		default:
		}
		slog.Info("sent shutdown signal")
	}
	// Rust returns an empty 200 body.
	_, _ = w.Write([]byte(""))
}

func (s *Server) handleErd(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Erd(r.Context())
	if err != nil {
		slog.Error("error while getting ERD data", "err", err)
		internalError(w)
		return
	}
	s.writeJSON(w, res)
}
