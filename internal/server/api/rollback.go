package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// CreateRollback implements POST /api/projects/{name}/rollback. Picks
// a target deployment (explicit --to=N, or the most recent
// successful deployment that isn't the current live one), verifies
// every cached image is still on disk, and enqueues a queued
// deployment row with rollback_of=<target>. The dispatcher picks it
// up and Orchestrator.rollbackRun does the cutover.
func (h *Handler) CreateRollback(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	var req cobaltapi.RollbackRequest
	if r.ContentLength > 0 {
		if err := readJSON(w, r, &req); err != nil {
			return
		}
	}

	current, err := h.DB.GetLastSuccessfulDeployment(r.Context(), p.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project has no successful deployment to roll back from")
		return
	}
	if err != nil {
		h.Log.Error("api: rollback: load current", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var target *store.Deployment
	if req.To > 0 {
		target, err = h.DB.GetDeploymentByNumber(r.Context(), p.ID, req.To)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound,
				"deployment #"+strconv.Itoa(req.To)+" not found for project "+p.Name)
			return
		}
		if err != nil {
			h.Log.Error("api: rollback: load target", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if target.Status != cobaltapi.StateSuccess {
			writeError(w, http.StatusBadRequest,
				"deployment #"+strconv.Itoa(req.To)+" did not succeed and cannot be rolled back to")
			return
		}
	} else {
		target, err = h.DB.PreviousSuccessfulDeployment(r.Context(), p.ID, current.ID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound,
				"no prior successful deployment to roll back to")
			return
		}
		if err != nil {
			h.Log.Error("api: rollback: previous successful", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if target.ID == current.ID {
		writeError(w, http.StatusConflict,
			"deployment #"+strconv.Itoa(target.Number)+" is already current; nothing to roll back to")
		return
	}

	if err := verifyTargetImagesExist(r, h, p.Name, target); err != nil {
		// Preserve the structured 410 from the helper.
		var imgErr *imageMissingError
		if errors.As(err, &imgErr) {
			writeError(w, http.StatusGone, imgErr.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, _, err := h.Queue.EnqueueRollback(r.Context(), p.ID, target.ID)
	if err != nil {
		h.Log.Error("api: rollback: enqueue", "error", err)
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
	writeJSON(w, deploymentToAPI(*dep))
}

// imageMissingError is the typed error verifyTargetImagesExist
// returns when a cached image is gone so the handler can map it to
// 410.
type imageMissingError struct{ msg string }

func (e *imageMissingError) Error() string { return e.msg }

// verifyTargetImagesExist probes every container service in the
// target's resolved cobaltfile to confirm its image tag is still on
// disk, refusing a rollback that would otherwise fail mid-cutover.
func verifyTargetImagesExist(r *http.Request, h *Handler, projectName string, target *store.Deployment) error {
	if target.ResolvedCobaltfile == nil {
		return &imageMissingError{
			msg: "deployment #" + strconv.Itoa(target.Number) + " has no recorded cobaltfile (run `cobalt deploy --commit <sha>` to rebuild)",
		}
	}
	if h.Docker == nil {
		return errors.New("daemon Docker client not configured")
	}
	cf, err := cobaltfile.Parse([]byte(*target.ResolvedCobaltfile))
	if err != nil {
		return &imageMissingError{
			msg: "deployment #" + strconv.Itoa(target.Number) + " cobaltfile is unreadable",
		}
	}
	checked := map[string]struct{}{}
	for _, svc := range cf.Services {
		if svc.Type != "" && svc.Type != cobaltfile.TypeContainer {
			continue
		}
		image := svc.Image
		if image == "" {
			image = "default"
		}
		tag := docker.InternalImageName(projectName, image, target.Number)
		if _, seen := checked[tag]; seen {
			continue
		}
		checked[tag] = struct{}{}
		ok, err := h.Docker.ImageExists(r.Context(), tag)
		if err != nil {
			return err
		}
		if !ok {
			return &imageMissingError{
				msg: "image for deployment #" + strconv.Itoa(target.Number) +
					" is no longer cached (" + tag + "); run `cobalt deploy --commit <sha>` to rebuild",
			}
		}
	}
	return nil
}
