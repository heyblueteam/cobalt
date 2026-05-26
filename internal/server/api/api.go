// Package api implements the cobalt daemon's HTTP handlers — every
// route under /api/ that the CLI talks to. Wiring (router + middleware
// chain) lives in router.go; per-resource handlers live in their own
// files (projects.go, env.go, ...).
//
// Response convention: bare types (`{...}` or `[...]`). Errors use the
// {"error": "<message>"} envelope. See plans/cobalt-9b-http-api.md.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// Handler is the per-package handler set. Constructor takes everything
// the resource handlers depend on; tests inject fakes via NewHandler.
type Handler struct {
	DB         *store.DB
	Caddy      *caddy.Client
	Docker     *docker.Client
	GitHub     *github.Client
	Queue      *deploy.Queue
	Dispatcher *deploy.Dispatcher
	Dedup      *webhookDedup
	Log        *slog.Logger
	// DataDir is the daemon's writable root. Used by streaming
	// endpoints to locate per-deployment log files. Empty disables
	// log streaming endpoints (they return 500).
	DataDir string
	// PublicHost is the daemon's public hostname (e.g. "cobalt.blue.cc").
	// Used to build manifest URLs. Empty falls back to the request's
	// Host header — fine for dev, but real deploys should set it.
	PublicHost string
	// Version is the daemon binary version string (set at build time).
	// Surfaced via GET /api/meta/info.
	Version string
	// StartedAt is when the daemon process began. Used to compute
	// uptime in GET /api/meta/info. Set by NewHandler.
	StartedAt time.Time
	// CronManager surfaces project-cron registration to the API
	// handlers (`crons list`, project-delete cleanup). nil disables
	// cron-related endpoints, which return empty lists.
	CronManager CronManager
}

// CronManager is the subset of *worker.CronManager the API needs.
// Defined here so we don't import the worker package directly and
// avoid a cycle if cron ever needs to call into the API surface.
type CronManager interface {
	ListForProject(projectName string) []CronView
	RemoveAllForProject(projectName string) error
}

// CronView is the read-only shape returned by CronManager.ListForProject
// for serialization. Mirrors worker.ProjectCronView; declared here to
// keep the api package independent.
type CronView struct {
	ServiceName      string
	Schedule         string
	Command          string
	DeploymentNumber int
	NextFireAt       time.Time
}

// HandlerOpts is the constructor input for NewHandler.
type HandlerOpts struct {
	DB          *store.DB
	Caddy       *caddy.Client
	Docker      *docker.Client
	GitHub      *github.Client
	Queue       *deploy.Queue
	Dispatcher  *deploy.Dispatcher
	Log         *slog.Logger
	DataDir     string
	PublicHost  string
	Version     string
	CronManager CronManager

	// WebhookDedupTTL controls the in-memory dedup window for
	// X-GitHub-Delivery. Zero means "use the package default" (10m).
	WebhookDedupTTL time.Duration
}

// DefaultWebhookDedupTTL is the default lookback for X-GitHub-Delivery
// dedup. GitHub's retry storms are over within seconds; 10 minutes is
// plenty of cushion.
const DefaultWebhookDedupTTL = 10 * time.Minute

// NewHandler constructs a Handler with all the wiring (notably the
// webhook dedup map) ready for use.
func NewHandler(opts HandlerOpts) *Handler {
	ttl := opts.WebhookDedupTTL
	if ttl == 0 {
		ttl = DefaultWebhookDedupTTL
	}
	return &Handler{
		DB:          opts.DB,
		Caddy:       opts.Caddy,
		Docker:      opts.Docker,
		GitHub:      opts.GitHub,
		Queue:       opts.Queue,
		Dispatcher:  opts.Dispatcher,
		Dedup:       newWebhookDedup(ttl),
		Log:         opts.Log,
		DataDir:     opts.DataDir,
		PublicHost:  opts.PublicHost,
		Version:     opts.Version,
		StartedAt:   time.Now(),
		CronManager: opts.CronManager,
	}
}

// errorResponse is the body of every non-2xx response.
type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON writes v as JSON with a 200 status. Uses w.WriteHeader
// implicitly via Encode; callers wanting a non-200 status should call
// w.WriteHeader BEFORE writeJSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// writeError emits a JSON error body with the given HTTP status.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

// readJSON decodes the request body into v. Returns nil on success;
// returns a 400 to the client and a non-nil error on parse failures so
// the caller can simply `return`.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return err
	}
	return nil
}

// projectFromPath looks up a project by the {name} URL parameter.
// On error or missing project the response is already written and
// the caller should simply return.
func (h *Handler) projectFromPath(w http.ResponseWriter, r *http.Request) (*store.Project, bool) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing project name")
		return nil, false
	}
	p, err := h.DB.GetProjectByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	if err != nil {
		h.Log.Error("api: lookup project", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	return p, true
}
