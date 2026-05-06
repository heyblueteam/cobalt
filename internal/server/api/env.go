package api

import (
	"errors"
	"net/http"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListEnv implements GET /api/projects/{name}/env.
func (h *Handler) ListEnv(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	vars, err := h.DB.ListEnvVars(r.Context(), p.ID)
	if err != nil {
		h.Log.Error("api: list env", "project_id", p.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.EnvVar, 0, len(vars))
	for _, v := range vars {
		out = append(out, cobaltapi.EnvVar{Key: v.Key, Value: v.Value})
	}
	writeJSON(w, out)
}

// SetEnv implements POST /api/projects/{name}/env. Idempotent bulk
// upsert. Optionally enqueues a redeploy when req.Redeploy is true.
//
// Returns the resulting full env list so callers don't need a second
// GET.
func (h *Handler) SetEnv(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	var req cobaltapi.EnvSetRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	for k := range req.Vars {
		if k == "" {
			writeError(w, http.StatusBadRequest, "env key must not be empty")
			return
		}
	}
	if err := h.DB.SetEnvVars(r.Context(), p.ID, req.Vars); err != nil {
		h.Log.Error("api: set env", "project_id", p.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Redeploy {
		if _, _, err := h.Queue.Enqueue(r.Context(), deploy.EnqueueRequest{ProjectID: p.ID}); err != nil {
			h.Log.Error("api: enqueue redeploy after env change", "error", err)
		} else if h.Dispatcher != nil {
			h.Dispatcher.Notify()
		}
	}
	vars, err := h.DB.ListEnvVars(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.EnvVar, 0, len(vars))
	for _, v := range vars {
		out = append(out, cobaltapi.EnvVar{Key: v.Key, Value: v.Value})
	}
	writeJSON(w, out)
}

// DeleteEnv implements DELETE /api/projects/{name}/env/{key}. Optional
// `?redeploy=true` enqueues a fresh deploy after the change lands.
func (h *Handler) DeleteEnv(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing env key")
		return
	}
	if err := h.DB.DeleteEnvVar(r.Context(), p.ID, key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "env var not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if r.URL.Query().Get("redeploy") == "true" {
		if _, _, err := h.Queue.Enqueue(r.Context(), deploy.EnqueueRequest{ProjectID: p.ID}); err != nil {
			h.Log.Error("api: enqueue redeploy after env delete", "error", err)
		} else if h.Dispatcher != nil {
			h.Dispatcher.Notify()
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
