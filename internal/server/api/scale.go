package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// GetScale implements GET /api/projects/{name}/scale. Returns the replica
// counts for every cobalt-managed service in the project's most recent
// successful deployment.
func (h *Handler) GetScale(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	dep, err := h.DB.GetLastSuccessfulDeployment(r.Context(), p.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project has no successful deployment")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	svcList, err := h.Docker.ListServicesForDeployment(r.Context(), p.ID, dep.Number)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	services := make([]cobaltapi.ScaleService, 0, len(svcList))
	for _, svc := range svcList {
		services = append(services, cobaltapi.ScaleService{
			Name:     parseServiceName(svc.Name, p.Name, dep.Number),
			Replicas: svc.Replicas,
		})
	}
	writeJSON(w, cobaltapi.ScaleInfo{
		Project:  p.Name,
		Services: services,
	})
}

// SetScale implements POST /api/projects/{name}/scale. Accepts a list of
// service name + replica count pairs and calls `docker service scale` for
// each. Returns the updated replica counts for every service in the
// deployment (mirroring GET /scale).
func (h *Handler) SetScale(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	dep, err := h.DB.GetLastSuccessfulDeployment(r.Context(), p.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project has no successful deployment")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var req cobaltapi.ScaleSetRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	for _, s := range req.Services {
		if s.Name == "" {
			writeError(w, http.StatusBadRequest, "service name must not be empty")
			return
		}
		if s.Replicas < 0 {
			writeError(w, http.StatusBadRequest, "replicas must not be negative")
			return
		}
		fullName := docker.ServiceName(p.Name, dep.Number, s.Name)
		if err := h.Docker.ScaleService(r.Context(), fullName, s.Replicas); err != nil {
			h.Log.Error("api: scale service", "service", fullName, "error", err)
			writeError(w, http.StatusInternalServerError, "scale failed: "+err.Error())
			return
		}
	}
	svcList, err := h.Docker.ListServicesForDeployment(r.Context(), p.ID, dep.Number)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	services := make([]cobaltapi.ScaleService, 0, len(svcList))
	for _, svc := range svcList {
		services = append(services, cobaltapi.ScaleService{
			Name:     parseServiceName(svc.Name, p.Name, dep.Number),
			Replicas: svc.Replicas,
		})
	}
	writeJSON(w, cobaltapi.ScaleInfo{
		Project:  p.Name,
		Services: services,
	})
}

// parseServiceName strips the cobalt name prefix from a docker service
// name to return the user-facing service name declared in cobaltfile.
// e.g. "myproject-5-web" + "myproject" + 5 → "web".
func parseServiceName(fullName, projectName string, deploymentNumber int) string {
	prefix := docker.ServiceName(projectName, deploymentNumber, "")
	return strings.TrimPrefix(fullName, prefix)
}
