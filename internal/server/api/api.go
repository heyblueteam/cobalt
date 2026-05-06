// Package api implements the cobalt daemon's HTTP handlers — every
// route under /api/ that the CLI talks to. Wiring (router + middleware
// chain) lives in router.go; per-resource handlers live in their own
// files (projects.go, env.go, ...).
//
// Response convention: bare types (`{...}` or `[...]`). Errors use the
// {"error": "<message>"} envelope. See plans/cobalt-9b-http-api.md.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/middleware"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// Handler is the per-package handler set. Constructor takes everything
// the resource handlers depend on; tests inject fakes via the same
// constructor.
type Handler struct {
	DB         *store.DB
	Caddy      *caddy.Client
	Queue      *deploy.Queue
	Dispatcher *deploy.Dispatcher
	Log        *slog.Logger
}

// errorResponse is the body of every non-2xx response.
type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON writes v as JSON with a 200 status. Uses w.WriteHeader
// implicitly via Encode; callers wanting a non-200 status should call
// w.WriteHeader BEFORE writeJSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// writeError emits a JSON error body with the given HTTP status.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

// readJSON decodes the request body into v. Returns nil on success;
// returns a 400 to the client and a non-nil error on parse failures so
// the caller can simply `return`.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return err
	}
	return nil
}

// projectFromPath looks up a project by the {name} URL parameter.
// On error or missing project the response is already written and
// the caller should simply return.
func (h *Handler) projectFromPath(w http.ResponseWriter, r *http.Request) (*store.Project, bool) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing project name")
		return nil, false
	}
	p, err := h.DB.GetProjectByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	if err != nil {
		h.Log.Error("api: lookup project", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	return p, true
}

// requestID is a small accessor for handlers that want the per-request
// id to attach to logs. Falls back to "" if not set.
func requestID(r *http.Request) string {
	return middleware.RequestIDFrom(r.Context())
}
