package api

import (
	"net/http"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListVolumes implements GET /api/projects/{name}/volumes. Returns
// the user-friendly volume names (the values from cobalt.json) plus
// the docker-side full name for diagnostics.
func (h *Handler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	project, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	full, err := h.Docker.ListVolumesForProject(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.Volume, 0, len(full))
	prefix := docker.VolumeName(project.ID, "") // "cobalt-volume-{id}-"
	for _, fullName := range full {
		short := strings.TrimPrefix(fullName, prefix)
		out = append(out, cobaltapi.Volume{Name: short, FullName: fullName})
	}
	writeJSON(w, out)
}

// ExportVolume implements POST /api/projects/{name}/volumes/{volume}/export.
// Streams the volume's contents as a binary tar to the response body.
//
// Method is POST (not GET) to match upstream and to discourage
// accidental browser navigation; the response is binary.
func (h *Handler) ExportVolume(w http.ResponseWriter, r *http.Request) {
	project, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	volumeName := r.PathValue("volume")
	if volumeName == "" {
		writeError(w, http.StatusBadRequest, "missing volume name")
		return
	}
	full := docker.VolumeName(project.ID, volumeName)

	// Docker's `--mount type=volume,source=X` silently creates an empty
	// volume named X when one doesn't exist, which would otherwise let
	// `cobalt volumes export --volume <typo>` ship back an empty tar
	// that an operator might mistake for a real backup. Validate first.
	exists, err := h.Docker.VolumeExists(r.Context(), full)
	if err != nil {
		h.Log.Error("api: export volume: probe", "volume", full, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "volume "+volumeName+" not found for project "+project.Name)
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+project.Name+`-`+volumeName+`.tar"`)
	if err := h.Docker.ExportVolume(r.Context(), full, w); err != nil {
		// Headers are already written; the client will see a truncated
		// stream. Log so operators can correlate failures.
		h.Log.Error("api: export volume", "volume", full, "error", err)
	}
}

// ImportVolume implements POST /api/projects/{name}/volumes/{volume}/import.
// Reads a binary tar from the request body and pours it into the
// volume. The volume must already exist (created by a deploy that
// references it in cobalt.json).
func (h *Handler) ImportVolume(w http.ResponseWriter, r *http.Request) {
	project, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	volumeName := r.PathValue("volume")
	if volumeName == "" {
		writeError(w, http.StatusBadRequest, "missing volume name")
		return
	}
	full := docker.VolumeName(project.ID, volumeName)

	// Same auto-create trap as ExportVolume — refuse to import into a
	// volume the project doesn't already declare, since the deploy is
	// the source of truth for what volumes a project should have.
	exists, err := h.Docker.VolumeExists(r.Context(), full)
	if err != nil {
		h.Log.Error("api: import volume: probe", "volume", full, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "volume "+volumeName+" not found for project "+project.Name)
		return
	}

	if err := h.Docker.ImportVolume(r.Context(), full, r.Body); err != nil {
		h.Log.Error("api: import volume", "volume", full, "error", err)
		writeError(w, http.StatusInternalServerError, "import failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
