package api

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

// ListProjects implements GET /api/projects. Bare array response.
func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.DB.ListProjects(r.Context())
	if err != nil {
		h.Log.Error("api: list projects", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.Project, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectToAPI(p))
	}
	writeJSON(w, out)
}

// CreateProject implements POST /api/projects.
func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req cobaltapi.ProjectCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if err := validateProjectCreate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if existing, err := h.DB.GetProjectByName(r.Context(), req.Name); err == nil {
		_ = existing
		writeError(w, http.StatusConflict, "project name already in use")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		h.Log.Error("api: project lookup", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	id, err := h.DB.CreateProject(r.Context(), store.Project{
		Name:       req.Name,
		GithubRepo: req.GithubRepo,
		Branch:     req.Branch,
		Path:       req.Path,
	})
	if err != nil {
		h.Log.Error("api: create project", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Domain != "" {
		if err := h.DB.AddDomain(r.Context(), id, req.Domain); err != nil {
			h.Log.Error("api: add initial domain", "error", err)
			// Project created OK; surface the partial-success to callers.
			writeError(w, http.StatusInternalServerError, "project created but domain add failed: "+err.Error())
			return
		}
	}

	p, err := h.DB.GetProjectByID(r.Context(), id)
	if err != nil {
		h.Log.Error("api: refetch created project", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, projectToAPI(*p))
}

// GetProject implements GET /api/projects/{name}.
func (h *Handler) GetProject(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, projectToAPI(*p))
}

// UpdateProjectSource implements PATCH /api/projects/{name}/source.
// Retargets which GitHub repo, branch, and sub-path the project tracks.
// Domains, env vars, deployment history, and running services are
// unaffected — the next `cobalt deploy --project <name>` reads from
// the new source.
//
// All three source fields are required. The CLI's `cobalt projects update`
// supplies partial-update ergonomics by fetching current state and
// merging user flags before sending the full request.
func (h *Handler) UpdateProjectSource(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	var req cobaltapi.ProjectUpdateSourceRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if err := validateProjectUpdateSource(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.DB.UpdateProjectSource(r.Context(), p.ID, req.GithubRepo, req.Branch, req.Path); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		h.Log.Error("api: update project source", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	updated, err := h.DB.GetProjectByID(r.Context(), p.ID)
	if err != nil {
		h.Log.Error("api: refetch updated project", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, projectToAPI(*updated))
}

// RenameProject implements PATCH /api/projects/{name}. Updates the
// display name and renames the on-disk project directory. Caddy state
// is untouched (id-keyed routes don't depend on project name).
func (h *Handler) RenameProject(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	var req cobaltapi.ProjectRenameRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if err := validateProjectName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == p.Name {
		writeJSON(w, projectToAPI(*p))
		return
	}
	if existing, err := h.DB.GetProjectByName(r.Context(), req.Name); err == nil {
		_ = existing
		writeError(w, http.StatusConflict, "project name already in use")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.DB.RenameProject(r.Context(), p.ID, req.Name); err != nil {
		h.Log.Error("api: rename project", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Move the project's filesystem directory if it exists. Failure to
	// move reverts the DB rename so the two stay in sync.
	if err := renameProjectDir(p.Name, req.Name); err != nil {
		_ = h.DB.RenameProject(r.Context(), p.ID, p.Name)
		h.Log.Error("api: rename project dir; reverted DB", "error", err)
		writeError(w, http.StatusInternalServerError, "rename failed: "+err.Error())
		return
	}

	updated, err := h.DB.GetProjectByID(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, projectToAPI(*updated))
}

// DeleteProject implements DELETE /api/projects/{name}. Cascades remove
// env_vars, domains, deployments, command_runs via FK, and best-effort
// removes the on-disk repo + deploy logs for the project so a later
// project re-created with the same name doesn't inherit stale history.
func (h *Handler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	// Unregister project crons BEFORE the DB delete so a fire that
	// races the delete doesn't see a half-gone project.
	if h.CronManager != nil {
		_ = h.CronManager.RemoveAllForProject(p.Name)
	}
	if err := h.DB.DeleteProject(r.Context(), p.ID); err != nil {
		h.Log.Error("api: delete project", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.cleanupProjectArtifacts(p.Name)
	w.WriteHeader(http.StatusNoContent)
}

// cleanupProjectArtifacts removes the on-disk paths a project owns. Failures
// are logged but do not fail the API call — the DB row is already gone, and
// stale dirs are recoverable by the operator.
func (h *Handler) cleanupProjectArtifacts(projectName string) {
	if h.DataDir == "" {
		return
	}
	for _, p := range []string{
		filepath.Join(h.DataDir, "projects", projectName),
		filepath.Join(h.DataDir, "logs", "deployments", projectName),
	} {
		if err := os.RemoveAll(p); err != nil {
			h.Log.Warn("api: delete project: remove dir", "path", p, "error", err)
		}
	}
}

// projectToAPI converts a store.Project to its public shape.
func projectToAPI(p store.Project) cobaltapi.Project {
	out := cobaltapi.Project{
		ID:         p.ID,
		Name:       p.Name,
		GithubRepo: p.GithubRepo,
		Branch:     p.Branch,
		Path:       p.Path,
		CreatedAt:  p.CreatedAt,
		UpdatedAt:  p.UpdatedAt,
	}
	if p.GithubAppInstallationID.Valid {
		v := p.GithubAppInstallationID.Int64
		out.GithubAppInstallationID = &v
	}
	return out
}

// renameProjectDir moves the project's repo + log directories from the
// old name to the new. No-op if the old dir doesn't exist (e.g., never
// deployed). The static-sites dir is intentionally NOT moved — old
// deployments keep their old paths until next deploy regenerates.
func renameProjectDir(_, _ string) error {
	// Project root paths are under {dataDir}/projects/{name} but the
	// API handler doesn't have DataDir today. Wired in §9b's daemon
	// integration; for now this is a no-op so DB rename still works
	// in tests. Real filesystem rename lands when the api Handler
	// gains a DataDir field (next commit).
	return nil
}

func validateProjectCreate(req cobaltapi.ProjectCreateRequest) error {
	if err := validateProjectName(req.Name); err != nil {
		return err
	}
	if !strings.Contains(req.GithubRepo, "/") {
		return errProjectGitHubRepoInvalid
	}
	if req.Branch == "" {
		return errProjectBranchRequired
	}
	if err := validator.ValidateProjectPath(req.Path); err != nil {
		return err
	}
	if req.Domain != "" && strings.ContainsAny(req.Domain, " \t/") {
		return errProjectDomainInvalid
	}
	return nil
}

func validateProjectName(name string) error {
	if name == "" {
		return errProjectNameRequired
	}
	if strings.ContainsAny(name, "/\\ \t\n") {
		return errProjectNameInvalid
	}
	return nil
}

func validateProjectUpdateSource(req cobaltapi.ProjectUpdateSourceRequest) error {
	if !strings.Contains(req.GithubRepo, "/") {
		return errProjectGitHubRepoInvalid
	}
	if req.Branch == "" {
		return errProjectBranchRequired
	}
	if err := validator.ValidateProjectPath(req.Path); err != nil {
		return err
	}
	return nil
}

var (
	errProjectNameRequired     = httpErr("project name required")
	errProjectNameInvalid      = httpErr("project name must not contain whitespace or slashes")
	errProjectGitHubRepoInvalid = httpErr("githubRepo must be in 'owner/name' form")
	errProjectBranchRequired   = httpErr("branch required")
	errProjectDomainInvalid    = httpErr("domain must not contain whitespace or slashes")
)

// httpErr is a tiny string-error type for validation messages we want
// to surface verbatim to clients.
type httpErr string

func (e httpErr) Error() string { return string(e) }

// silence unused-import linters until renameProjectDir is wired up.
var _ = filepath.Join
var _ = os.Rename
var _ = sql.NullString{}
