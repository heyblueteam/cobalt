package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Orchestrator implements deploy.Runner. It composes Preparer + Builder
// (from 8b) with the cutover/rollback primitives in this package to drive
// a deployment from queued → live, honoring the four improvements over
// upstream identified in the deploy-flow audit (PATCH verify, healthcheck
// status, two-phase commit, build cache isolation).
type Orchestrator struct {
	DB        *store.DB
	Docker    *docker.Client
	Caddy     *caddy.Client
	Preparer  Preparer
	Builder   Builder
	DataDir   string
	Log       *slog.Logger
	LogWriter io.Writer // where build / hook stdout flows; usually a per-deploy log file
}

// Run satisfies the deploy.Runner interface. It receives a Deployment row
// already in StateFetching (the dispatcher transitions queued → fetching
// before calling). The dispatcher writes the terminal status (success /
// failed / canceled) based on this method's return value.
func (o *Orchestrator) Run(ctx context.Context, dep store.Deployment) error {
	log := o.Log.With("deployment_id", dep.ID, "project_id", dep.ProjectID, "number", dep.Number)

	project, err := o.getProject(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("deploy: get project: %w", err)
	}
	log = log.With("project", project.Name)

	// Open the deploy log file. If the caller (tests) supplied a writer
	// directly, honor that; otherwise stream to disk under DataDir so
	// /api/deployments/{id}/logs (lands in §9) can read it back later.
	out, closeOut, err := o.openLog(*project, dep)
	if err != nil {
		log.Warn("could not open deploy log; falling back to discard", "error", err)
		out = io.Discard
		closeOut = func() {}
	}
	defer closeOut()

	envVars, err := o.DB.EnvVarMap(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("deploy: env vars: %w", err)
	}

	// PHASE 1 — prepare. Reversible, no Caddy touch.

	ws, err := o.Preparer.Prepare(ctx, *project, dep)
	if err != nil {
		return fmt.Errorf("deploy: prepare: %w", err)
	}
	log.Info("repo prepared", "commit", ws.Commit)

	// Persist the resolved cobaltfile so the §8d Caddy convergence
	// reconciler can read authoritative desired state without re-cloning
	// the repo. Failure here is non-fatal — the deploy still proceeds and
	// the next deploy will populate the row.
	if raw, err := json.Marshal(ws.Cobaltfile); err == nil {
		if err := o.DB.SetResolvedCobaltfile(ctx, dep.ID, string(raw)); err != nil {
			log.Warn("set resolved cobaltfile", "error", err)
		}
	}

	if err := o.DB.SetDeploymentStatus(ctx, dep.ID, cobaltapi.StateBuilding); err != nil {
		log.Warn("set status building", "error", err)
	}

	built, err := o.Builder.Build(ctx, *project, dep, ws)
	if err != nil {
		return fmt.Errorf("deploy: build: %w", err)
	}
	log.Info("build complete", "services", len(built))

	if err := EnsureMainNetwork(ctx, o.Docker); err != nil {
		return fmt.Errorf("deploy: ensure main network: %w", err)
	}

	staticDir := o.Caddy.StaticSiteDeploymentPath(project.Name, dep.Number)
	if _, err := runGenerators(ctx, o.Docker, *project, dep, built, envVars, staticDir, out, out); err != nil {
		return fmt.Errorf("deploy: generators: %w", err)
	}

	if err := runBeforeHook(ctx, o.Docker, *project, dep, ws.Cobaltfile, envVars, out, out); err != nil {
		return fmt.Errorf("deploy: before-hook: %w", err)
	}

	deploymentNetwork := docker.NetworkName(project.Name, dep.Number)
	if err := o.ensureDeploymentNetwork(ctx, *project, dep); err != nil {
		return fmt.Errorf("deploy: deployment network: %w", err)
	}

	if err := o.DB.SetDeploymentStatus(ctx, dep.ID, cobaltapi.StateSwapping); err != nil {
		log.Warn("set status swapping", "error", err)
	}

	startedServices, err := startServicesPhase(ctx, o.Docker, *project, dep, built, envVars, deploymentNetwork)
	if err != nil {
		// Phase 1 failure: stop services we did start, no Caddy touch.
		_ = stopServices(context.Background(), o.Docker, startedServices)
		return err
	}
	if err := waitHealthyAll(ctx, o.Docker, *project, dep, built); err != nil {
		_ = stopServices(context.Background(), o.Docker, startedServices)
		return err
	}

	// PHASE 2 — commit. Caddy cutover, atomic from the public's POV.

	if err := commitCaddySwap(ctx, o.Caddy, o.DB, *project, dep, ws.Cobaltfile); err != nil {
		// Try to revert; whether or not revert succeeds, kill the new
		// services so they don't linger.
		revertCaddySwap(context.Background(), log, o.Caddy, o.DB, *project, ws.Cobaltfile)
		_ = stopServices(context.Background(), o.Docker, startedServices)
		return fmt.Errorf("deploy: commit caddy: %w", err)
	}

	// After-hook: best effort. If the deploy is live, a failed after-hook
	// is a warning, not a rollback trigger.
	if err := runAfterHook(ctx, o.Docker, *project, dep, ws.Cobaltfile, envVars, out, out); err != nil {
		log.Warn("after-hook failed (deployment is live)", "error", err)
	}

	// POST-SUCCESS — clean up old services. Best effort.
	cleanupOldServices(context.Background(), log, o.Docker, *project, dep)

	log.Info("deploy complete")
	return nil
}

// getProject fetches the project for a deployment by id.
func (o *Orchestrator) getProject(ctx context.Context, id int64) (*store.Project, error) {
	return o.DB.GetProjectByID(ctx, id)
}

// openLog returns a writer for the deploy's stdout/stderr capture, plus a
// close function the caller defers. If LogWriter is set (tests), it's used
// directly with a no-op close. Otherwise OpenDeployLog opens the file
// under DataDir; if that fails, the caller falls back to io.Discard.
func (o *Orchestrator) openLog(project store.Project, dep store.Deployment) (io.Writer, func(), error) {
	if o.LogWriter != nil {
		return o.LogWriter, func() {}, nil
	}
	if o.DataDir == "" {
		return io.Discard, func() {}, nil
	}
	wc, err := OpenDeployLog(o.DataDir, project.Name, dep.Number)
	if err != nil {
		return nil, nil, err
	}
	return wc, func() { _ = wc.Close() }, nil
}

// ensureDeploymentNetwork creates the per-deployment network if missing
// (always missing for a new deployment; the helper is idempotent for
// completeness / retries).
func (o *Orchestrator) ensureDeploymentNetwork(ctx context.Context, project store.Project, dep store.Deployment) error {
	name := docker.NetworkName(project.Name, dep.Number)
	exists, err := o.Docker.NetworkExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return o.Docker.CreateNetwork(ctx, project.ID, project.Name, dep.Number)
}
