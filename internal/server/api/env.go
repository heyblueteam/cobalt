package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListEnv implements GET /api/projects/{name}/env. Each entry's
// `stale` flag is true when the var was written after the project's
// last successful deployment started — i.e. the running containers
// were built with a different (or absent) value and haven't picked
// up the change yet. Always false until the project has at least
// one successful deploy.
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

	// Find the live deployment's start time for the staleness
	// computation. No-deploy yet → no staleness signal possible.
	var liveStartedAt int64
	if dep, err := h.DB.GetLastSuccessfulDeployment(r.Context(), p.ID); err == nil && dep.StartedAt != nil {
		liveStartedAt = *dep.StartedAt
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.Log.Warn("api: list env: probe live deployment", "error", err)
	}

	out := make([]cobaltapi.EnvVar, 0, len(vars))
	for _, v := range vars {
		entry := cobaltapi.EnvVar{
			Key:       v.Key,
			Value:     v.Value,
			UpdatedAt: v.UpdatedAt,
		}
		if liveStartedAt > 0 && v.UpdatedAt > liveStartedAt {
			entry.Stale = true
		}
		out = append(out, entry)
	}
	writeJSON(w, out)
}

// GetEnv implements GET /api/projects/{name}/env/{key}. Returns a single
// env var's key and value, or 404 if not found.
func (h *Handler) GetEnv(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing env key")
		return
	}
	envVar, err := h.DB.GetEnvVar(r.Context(), p.ID, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "env var not found")
			return
		}
		h.Log.Error("api: get env", "project_id", p.ID, "key", key, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, cobaltapi.EnvVar{Key: envVar.Key, Value: envVar.Value})
}

// SetEnv implements POST /api/projects/{name}/env. Idempotent bulk
// upsert. Optionally enqueues a redeploy when req.Redeploy is true.
//
// Returns the rows that were just upserted (not the project's whole
// env list), sorted by key. Callers that want the full state issue a
// follow-up GET; mixing the two would mislead operators after a
// partial update — `cobalt env set FOO=bar` would echo every other
// var on the project too.
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
	keys := make([]string, 0, len(req.Vars))
	for k := range req.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]cobaltapi.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, cobaltapi.EnvVar{Key: k, Value: req.Vars[k]})
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
