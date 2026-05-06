package api

import (
	"net/http"
)

// Register attaches every §9b-i route onto mux. All routes live under
// /api/ and are expected to be wrapped in BearerAuth by the caller.
func (h *Handler) Register(mux *http.ServeMux) {
	// Projects
	mux.HandleFunc("GET /api/projects", h.ListProjects)
	mux.HandleFunc("POST /api/projects", h.CreateProject)
	mux.HandleFunc("GET /api/projects/{name}", h.GetProject)
	mux.HandleFunc("PATCH /api/projects/{name}", h.RenameProject)
	mux.HandleFunc("DELETE /api/projects/{name}", h.DeleteProject)

	// Env vars
	mux.HandleFunc("GET /api/projects/{name}/env", h.ListEnv)
	mux.HandleFunc("POST /api/projects/{name}/env", h.SetEnv)
	mux.HandleFunc("DELETE /api/projects/{name}/env/{key}", h.DeleteEnv)

	// Domains
	mux.HandleFunc("GET /api/projects/{name}/domains", h.ListDomains)
	mux.HandleFunc("POST /api/projects/{name}/domains", h.AddDomain)
	mux.HandleFunc("DELETE /api/projects/{name}/domains/{domain}", h.RemoveDomain)

	// Deployments
	mux.HandleFunc("GET /api/projects/{name}/deployments", h.ListDeployments)
	mux.HandleFunc("POST /api/projects/{name}/deployments", h.CreateDeployment)
	mux.HandleFunc("GET /api/deployments/{id}", h.GetDeployment)
	mux.HandleFunc("POST /api/deployments/{id}/cancel", h.CancelDeployment)
}
