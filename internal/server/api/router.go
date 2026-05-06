package api

import (
	"net/http"
)

// Register attaches every authenticated /api/ route onto mux. The mux
// itself is expected to be wrapped in BearerAuth by the caller.
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

	// GitHub apps & repos
	mux.HandleFunc("GET /api/github-apps", h.ListGithubApps)
	mux.HandleFunc("GET /api/github-app-repos", h.ListGithubAppRepos)
	mux.HandleFunc("POST /api/github-apps/prune", h.PruneGithubApps)
	mux.HandleFunc("POST /api/github-apps/create", h.CreatePendingApp)

	// Streaming
	mux.HandleFunc("GET /api/deployments/{id}/output", h.DeploymentOutput)
	mux.HandleFunc("GET /api/projects/{name}/logs", h.ProjectLogs)
}

// RegisterPublic attaches public (unauthenticated) routes onto mux.
// These are the GitHub-facing endpoints that don't go through bearer
// auth: webhook receiver and manifest-flow callbacks.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/github", h.WebhookGithub)
	mux.HandleFunc("GET /github-apps/{id}/create", h.ManifestForm)
	mux.HandleFunc("GET /github-apps/{id}/created", h.ManifestCreated)
}
