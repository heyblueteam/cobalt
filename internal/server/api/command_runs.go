package api

import (
	"net/http"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListCommandRuns implements GET /api/projects/{name}/command-runs.
// Returns the project's `cobalt run` audit log, newest first. Optional
// `?limit=N` caps the result; default 50.
func (h *Handler) ListCommandRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	rows, err := h.DB.ListCommandRunsForProject(r.Context(), p.ID, limit)
	if err != nil {
		h.Log.Error("list command_runs", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.CommandRun, 0, len(rows))
	for _, c := range rows {
		out = append(out, commandRunToAPI(c))
	}
	writeJSON(w, out)
}

func commandRunToAPI(c store.CommandRun) cobaltapi.CommandRun {
	return cobaltapi.CommandRun{
		ID:         c.ID,
		ProjectID:  c.ProjectID,
		APIKeyID:   c.APIKeyID,
		Service:    c.Service,
		Command:    c.Command,
		Status:     c.Status,
		ExitCode:   c.ExitCode,
		TTY:        c.TTY,
		CreatedAt:  c.CreatedAt,
		FinishedAt: c.FinishedAt,
	}
}
