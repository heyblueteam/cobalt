package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListDeployments implements GET /api/projects/{name}/deployments.
// Returns most-recent-first. Optional `?limit=N` caps the result.
func (h *Handler) ListDeployments(w http.ResponseWriter, r *http.Request) {
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
	deps, err := h.DB.ListDeploymentsForProject(r.Context(), p.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.Deployment, 0, len(deps))
	for _, d := range deps {
		out = append(out, deploymentToAPI(d))
	}
	writeJSON(w, out)
}

// CreateDeployment implements POST /api/projects/{name}/deployments.
// Enqueues a new deploy and returns 202 Accepted with the deployment row.
func (h *Handler) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	var req cobaltapi.DeploymentCreateRequest
	if r.ContentLength > 0 {
		if err := readJSON(w, r, &req); err != nil {
			return
		}
	}

	enq := deploy.EnqueueRequest{
		ProjectID:          p.ID,
		CommitSHA:          req.Commit,
		NoCache:            req.NoCache,
		CobaltfileOverride: req.CobaltfileOverride,
	}
	if err := deploy.Validate(r.Context(), h.DB, enq); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var cancelledID int64
	if req.Force && h.Dispatcher != nil {
		active, err := h.DB.ActiveDeploymentForProject(r.Context(), p.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			h.Log.Error("api: lookup active deploy for force", "project_id", p.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if active != nil {
			if active.Status == cobaltapi.StateSwapping {
				writeError(w, http.StatusConflict,
					fmt.Sprintf("in-flight deploy %d is in cutover (swapping); --force not allowed at this stage", active.ID))
				return
			}
			if err := h.Dispatcher.Cancel(r.Context(), active.ID); err != nil {
				if errors.Is(err, deploy.ErrCancelDuringCutover) {
					writeError(w, http.StatusConflict,
						fmt.Sprintf("in-flight deploy %d entered cutover; --force not allowed", active.ID))
					return
				}
				if !errors.Is(err, deploy.ErrNotInFlight) && !errors.Is(err, deploy.ErrNotTracked) {
					h.Log.Warn("api: force-cancel in-flight", "id", active.ID, "error", err)
				}
			} else {
				cancelledID = active.ID
			}
		}
	}

	id, _, err := h.Queue.Enqueue(r.Context(), enq)
	if err != nil {
		h.Log.Error("api: enqueue deploy", "project_id", p.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if h.Dispatcher != nil {
		h.Dispatcher.Notify()
	}
	dep, err := h.DB.GetDeployment(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, cobaltapi.DeploymentCreateResponse{
		Deployment:          deploymentToAPI(*dep),
		CancelledInflightId: cancelledID,
	})
}

// GetDeployment implements GET /api/deployments/{id}.
func (h *Handler) GetDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}
	dep, err := h.DB.GetDeployment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, deploymentToAPI(*dep))
}

// CancelDeployment implements POST /api/deployments/{id}/cancel. For
// queued rows it transitions directly to canceled. For in-flight rows
// it cancels the dispatcher's runner context; the dispatcher writes the
// final canceled status when the runner returns.
func (h *Handler) CancelDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}
	cancelInFlight, err := h.Queue.Cancel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cancelInFlight && h.Dispatcher != nil {
		if err := h.Dispatcher.Cancel(r.Context(), id); err != nil {
			if errors.Is(err, deploy.ErrCancelDuringCutover) {
				writeError(w, http.StatusConflict,
					fmt.Sprintf("deploy %d is in cutover (swapping); cancel not allowed at this stage", id))
				return
			}
			h.Log.Warn("api: dispatcher cancel signal", "id", id, "error", err)
		}
	}
	dep, err := h.DB.GetDeployment(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, deploymentToAPI(*dep))
}

func deploymentToAPI(d store.Deployment) cobaltapi.Deployment {
	out := cobaltapi.Deployment{
		ID:        d.ID,
		ProjectID: d.ProjectID,
		Number:    d.Number,
		Status:    d.Status,
		NoCache:   d.NoCache,
		CreatedAt: d.CreatedAt,
	}
	if d.CommitSHA != nil {
		out.CommitSHA = *d.CommitSHA
	}
	if d.RollbackOf != nil {
		out.RollbackOf = *d.RollbackOf
	}
	if d.StartedAt != nil {
		out.StartedAt = *d.StartedAt
	}
	if d.FinishedAt != nil {
		out.FinishedAt = *d.FinishedAt
	}
	return out
}
