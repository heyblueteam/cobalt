package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/projectlock"
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
	// CronManager, when non-nil, is reconciled after every successful
	// cutover so the just-deployed cobaltfile's `type: cron` services
	// get registered with the scheduler. nil in unit tests that don't
	// exercise the cron path.
	CronManager CronReconciler
	// DataPlaneProber, when non-nil, gates each container cutover on a real
	// request through Caddy's own listener confirming the running router
	// serves the new deployment (not just the admin config tree, which can
	// lag). Production wires the docker client; left nil in unit tests that
	// don't exercise the data-plane gate, so the cutover path stays hermetic.
	DataPlaneProber ReadinessProber
}

// CronReconciler is the cron-side surface the orchestrator calls
// after a successful cutover. *worker.CronManager satisfies this.
type CronReconciler interface {
	Reconcile(ctx context.Context, project store.Project, dep store.Deployment, cf *cobaltfile.Cobaltfile) error
}

// Run satisfies the deploy.Runner interface. It receives a Deployment row
// already in StateFetching (the dispatcher transitions queued → fetching
// before calling). The dispatcher writes the terminal status (success /
// failed / canceled) based on this method's return value.
func (o *Orchestrator) Run(ctx context.Context, dep store.Deployment) (err error) {
	log := o.Log.With("deployment_id", dep.ID, "project_id", dep.ProjectID, "number", dep.Number)

	project, err := o.getProject(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("deploy: get project: %w", err)
	}
	log = log.With("project", project.Name)

	// Open the deploy log file. If the caller (tests) supplied a writer
	// directly, honor that; otherwise stream to disk under DataDir so
	// /api/deployments/{id}/logs (lands in §9) can read it back later.
	out, closeOut, openErr := o.openLog(*project, dep)
	if openErr != nil {
		log.Warn("could not open deploy log; falling back to discard", "error", openErr)
		out = io.Discard
		closeOut = func() {}
	}
	defer closeOut()

	// Header + footer so even a no-build deploy (all services declare
	// an explicit image, no Dockerfile) leaves a useful trace in the
	// log file. The deferred trailer surfaces any returned error and
	// stamps the elapsed time so operators can spot slow deploys at a
	// glance, and `cobalt deployments output` doesn't need shell
	// access to the host to explain failures.
	deployKind := "deploy"
	if dep.RollbackOf != nil {
		deployKind = "rollback"
	}
	header := fmt.Sprintf("%s #%d for project %q", deployKind, dep.Number, project.Name)
	startTime := time.Now()
	fmt.Fprintf(out, "==> %s started\n", header)
	defer func() {
		elapsed := time.Since(startTime).Round(time.Second)
		if err != nil {
			fmt.Fprintf(out, "❌ %s\n", err)
			fmt.Fprintf(out, "❌ %s failed (%s)\n", deployKind, elapsed)
		} else {
			fmt.Fprintf(out, "✅ %s #%d complete (%s)\n", deployKind, dep.Number, elapsed)
		}
	}()

	envVars, err := o.DB.EnvVarMap(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("deploy: env vars: %w", err)
	}

	// Rollback fork: skip prepare + build, reconstruct BuiltService
	// list from the target deployment's stored cobaltfile + image
	// tag pattern, and run the cutover phase only. See rollback.go.
	if dep.RollbackOf != nil {
		return o.rollbackRun(ctx, log, project, dep, envVars, out)
	}

	// PHASE 1 — prepare. Reversible, no Caddy touch.

	fmt.Fprintf(out, "📥 fetching from github.com/%s\n", project.GithubRepo)
	prepStart := time.Now()
	ws, err := o.Preparer.Prepare(ctx, *project, dep)
	if err != nil {
		return fmt.Errorf("deploy: prepare: %w", err)
	}
	log.Info("repo prepared", "commit", ws.Commit)
	fmt.Fprintf(out, "✅ checked out %s (%s)\n",
		shortSHA(ws.Commit), time.Since(prepStart).Round(time.Second))

	// Persist the resolved cobaltfile so the §8d Caddy convergence
	// reconciler can read authoritative desired state without re-cloning
	// the repo. Failure here is non-fatal — the deploy still proceeds and
	// the next deploy will populate the row.
	if raw, err := json.Marshal(ws.Cobaltfile); err == nil {
		if err := o.DB.SetResolvedCobaltfile(ctx, dep.ID, string(raw)); err != nil {
			log.Warn("set resolved cobaltfile", "error", err)
		}
	}

	// Preflight: a `web` service needs at least one domain attached so
	// commitCaddySwap has somewhere to point traffic. Without this
	// check the deploy builds the image, starts services, then dies at
	// the swap step with a confusing `unknown object ID
	// cobalt-project-handler-N`. Fail early with an actionable error.
	//
	// Exception: `exposedInternally: true` web services are
	// deliberately internal-only — other projects reach them via the
	// cobalt-main DNS alias `{project}-{service}`, never via Caddy /
	// a public domain. The swap step is skipped for these too, so the
	// "no domain → broken swap" rationale doesn't apply.
	if web, hasWeb := ws.Cobaltfile.Services["web"]; hasWeb && !web.ExposedInternally {
		domains, err := o.DB.ListDomainsForProject(ctx, project.ID)
		if err != nil {
			return fmt.Errorf("deploy: list domains: %w", err)
		}
		if len(domains) == 0 {
			return fmt.Errorf("deploy: project %q has a public web service but no domains attached; add one with `cobalt domains add <name>` and redeploy, or set `exposedInternally: true` on the service if it's reached only by other cobalt projects", project.Name)
		}
	}

	if err := o.DB.SetDeploymentStatus(ctx, dep.ID, cobaltapi.StateBuilding); err != nil {
		log.Warn("set status building", "error", err)
	}

	built, err := o.Builder.Build(ctx, *project, dep, ws, out)
	if err != nil {
		return fmt.Errorf("deploy: build: %w", err)
	}
	log.Info("build complete", "services", len(built))

	return o.cutover(ctx, log, project, dep, built, ws.Cobaltfile, envVars, out)
}

// Cutover runs the post-build phase of a deployment: ensure networks,
// run generators + before-hook, start docker services, wait healthy,
// swap Caddy, run the after-hook, clean up old services. This is the
// shared path between a fresh deploy (called internally from Run) and
// a rollback (which calls Cutover directly with services reconstructed
// from a prior deployment's cached image).
//
// envVars is the project's CURRENT env-var state, not a per-deploy
// snapshot. Rollback uses today's env, not the env that existed when
// the target was originally built.
func (o *Orchestrator) Cutover(
	ctx context.Context,
	project *store.Project,
	dep store.Deployment,
	built []BuiltService,
	cf *cobaltfile.Cobaltfile,
	envVars map[string]string,
	out io.Writer,
) error {
	log := o.Log.With("deployment_id", dep.ID, "project_id", dep.ProjectID,
		"number", dep.Number, "project", project.Name)
	if dep.RollbackOf != nil {
		log = log.With("rollback_of", *dep.RollbackOf)
	}
	return o.cutover(ctx, log, project, dep, built, cf, envVars, out)
}

func (o *Orchestrator) cutover(
	ctx context.Context,
	log *slog.Logger,
	project *store.Project,
	dep store.Deployment,
	built []BuiltService,
	cf *cobaltfile.Cobaltfile,
	envVars map[string]string,
	out io.Writer,
) (result error) {
	// Rollback uses this same path. Keep service creation, traffic cutover and
	// post-success cleanup exclusive with the scheduled reaper for this project.
	unlock := projectlock.Lock(project.ID)
	defer unlock()

	var probe retirementProber
	if o.DataPlaneProber != nil {
		probe = CaddyDataPlaneProber{Prober: o.DataPlaneProber}
	}
	if err := waitForWebCapacity(ctx, log, o.DB, o.Docker, o.Caddy, probe, *project, cf, out, 2*time.Second); err != nil {
		return err
	}

	if err := EnsureMainNetwork(ctx, o.Docker); err != nil {
		return fmt.Errorf("deploy: ensure main network: %w", err)
	}

	if err := validateStablePrivateAlias(ctx, o.Docker, *project, cf); err != nil {
		return fmt.Errorf("deploy: private alias preflight: %w", err)
	}
	previousStable := false
	var previousDeployment *store.Deployment
	var previousFile *cobaltfile.Cobaltfile
	if previous, err := o.DB.GetLastSuccessfulDeployment(ctx, project.ID); err == nil {
		previousDeployment = previous
		if previous.ResolvedCobaltfile != nil {
			previousCF, parseErr := cobaltfile.Parse([]byte(*previous.ResolvedCobaltfile))
			if parseErr != nil {
				return parseErr
			}
			previousFile = previousCF
			previousStable = previousCF.UsesStablePublicWeb()
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := validateStableRecoveryTransition(previousStable, cf, built); err != nil {
		return err
	}

	staticDir := o.Caddy.StaticSiteDeploymentPath(project.Name, dep.Number)
	if _, err := runGenerators(ctx, o.Docker, *project, dep, built, envVars, staticDir, out, out); err != nil {
		return fmt.Errorf("deploy: generators: %w", err)
	}

	if err := runBeforeHook(ctx, o.Docker, *project, dep, cf, envVars, out, out); err != nil {
		return fmt.Errorf("deploy: before-hook: %w", err)
	}

	deploymentNetwork := docker.NetworkName(project.Name, dep.Number)
	if err := o.ensureDeploymentNetwork(ctx, *project, dep); err != nil {
		return fmt.Errorf("deploy: deployment network: %w", err)
	}

	if err := o.DB.SetDeploymentStatus(ctx, dep.ID, cobaltapi.StateSwapping); err != nil {
		log.Warn("set status swapping", "error", err)
	}

	stableTx, err := prepareStableWebTransaction(ctx, o.Docker, *project, dep, cf, built, envVars, previousStable)
	if err != nil {
		return fmt.Errorf("deploy: snapshot stable web: %w", err)
	}
	var failedNames []string
	routeTouched := false
	defer func() {
		if result == nil || stableTx == nil {
			return
		}
		recoveryCtx, cancel := context.WithTimeout(context.Background(), stableTx.recoveryBudget(cf))
		defer cancel()
		// When leaving stable web, numbered replacements do not belong to the old
		// main-only dependency topology. Remove their alias owners before restore.
		if !stableTx.incomingStable {
			if err := stopServices(recoveryCtx, o.Docker, failedNames); err != nil {
				result = errors.Join(result, err)
				return
			}
			failedNames = nil
		}
		if err := stableTx.restore(recoveryCtx, o.Docker); err != nil {
			result = errors.Join(result, fmt.Errorf("stable web recovery failed; failed dependencies retained; operator action required: %w", err))
			return
		}
		if previousDeployment != nil && previousFile != nil {
			if err := waitHTTPReady(recoveryCtx, o.Docker, *project, *previousDeployment, previousFile, out); err != nil {
				result = errors.Join(result, fmt.Errorf("previous web readiness recovery failed; dependencies retained: %w", err))
				return
			}
			if previousFile.Services["web"].ExposedInternally && routeTouched {
				if err := o.Caddy.RemoveProjectRoute(recoveryCtx, project.ID); err != nil {
					result = errors.Join(result, err)
					return
				}
			} else if err := commitCaddySwap(recoveryCtx, o.Caddy, o.DB, *project, *previousDeployment, previousFile); err != nil {
				result = errors.Join(result, fmt.Errorf("previous route recovery failed; dependencies retained: %w", err))
				return
			}
		} else if previousDeployment == nil && routeTouched {
			if err := o.Caddy.RemoveProjectRoute(recoveryCtx, project.ID); err != nil {
				result = errors.Join(result, err)
				return
			}
		}
		if err := stopServices(recoveryCtx, o.Docker, failedNames); err != nil {
			result = errors.Join(result, err)
		}
	}()

	var publicStop *publicStopFirst
	if publicStopFirstEnabled(cf) {
		var err error
		publicStop, err = preparePublicStopFirst(ctx, o.Docker, o.DB, *project, dep)
		if err != nil {
			return fmt.Errorf("deploy: stopFirst preflight: %w", err)
		}
		defer func() {
			if result == nil {
				return
			}
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 2*HealthcheckTimeout)
			defer cancel()
			err := publicStop.recover(recoveryCtx, o.Docker, out)
			if err == nil && publicStop.previous != nil {
				err = waitHTTPReady(recoveryCtx, o.Docker, *project, *publicStop.previous, publicStop.previousFile, out)
				if err == nil {
					err = commitCaddySwap(recoveryCtx, o.Caddy, o.DB, *project, *publicStop.previous, publicStop.previousFile)
				}
			}
			if err != nil {
				result = errors.Join(result, fmt.Errorf("public stopFirst recovery failed; operator action required: %w", err))
			} else if publicStop.previous != nil {
				fmt.Fprintln(out, "↩ previous public service and route restored (data/schema changes are not undone)")
			}
		}()
		if err := publicStop.stopOld(ctx, o.Docker, out); err != nil {
			return err
		}
	}
	// Public stop-first owns its web cleanup so recovery can wait for real
	// process exit while the service object is still inspectable.
	cleanupFailed := func(names []string) {
		if stableTx != nil {
			failedNames = append(failedNames, names...)
			return
		}
		if publicStop == nil {
			_ = stopServices(context.Background(), o.Docker, names)
			return
		}
		for _, name := range names {
			if publicStop != nil && name == publicStop.newName {
				continue
			}
			_ = o.Docker.RemoveService(context.Background(), name)
		}
	}

	// stopFirst services surrender their old generation before the new one
	// starts — host-mode published ports can't be held by two generations at
	// once. Anything stopped here is down until the new service is healthy.
	if err := stopFirstPhase(ctx, o.Docker, *project, dep, built, out); err != nil {
		return err
	}

	stableWeb := cf.UsesStablePublicWeb()
	if cf.StablePublicWeb && !stableWeb {
		reason := cf.StablePublicWebUnavailableReason()
		fmt.Fprintf(out, "⚠ stablePublicWeb unavailable; using deployment-numbered routing: %s\n", reason)
		log.Warn("stable public web unavailable", "project_id", project.ID, "reason", reason)
	}
	startedServices, err := startServicesPhase(ctx, o.Docker, *project, dep, built, envVars, deploymentNetwork, stableWeb, out, stableTx)
	if err != nil {
		// Phase 1 failure: stop services we did start, no Caddy touch.
		cleanupFailed(startedServices)
		return err
	}
	if err := waitHealthyAll(ctx, o.Docker, *project, dep, built, stableWeb, out); err != nil {
		cleanupFailed(startedServices)
		return err
	}
	// Swarm says the task is running, but "running" only means the
	// process started — the app may still be opening its socket, loading
	// config, warming a cache, etc. Probe the web service's port from
	// inside cobalt-caddy before we route traffic; otherwise we'll cut
	// over to a backend that 502s.
	if err := waitHTTPReady(ctx, o.Docker, *project, dep, cf, out); err != nil {
		cleanupFailed(startedServices)
		return err
	}

	settleCtx, settleCancel := context.WithTimeout(ctx, stableTx.recoveryBudget(cf))
	err = stableTx.settle(settleCtx, o.Docker)
	if err == nil && (stableTx == nil || stableTx.incomingStable) {
		err = stableTx.activate(settleCtx, o.Docker)
	}
	if err == nil {
		err = stableTx.settle(settleCtx, o.Docker)
	}
	settleCancel()
	if err != nil {
		cleanupFailed(startedServices)
		return fmt.Errorf("deploy: stable web convergence: %w", err)
	}

	// PHASE 2 — commit. Caddy cutover, atomic from the public's POV.

	fmt.Fprintf(out, "🌍 routing traffic to deployment #%d\n", dep.Number)
	routeTouched = true
	if err := commitCaddySwap(ctx, o.Caddy, o.DB, *project, dep, cf); err != nil {
		// Try to revert; whether or not revert succeeds, kill the new
		// services so they don't linger.
		fmt.Fprintf(out, "↩️  traffic swap failed, reverting\n")
		if publicStop == nil && stableTx == nil {
			revertCaddySwap(context.Background(), log, o.Caddy, o.DB, *project, cf)
		}
		cleanupFailed(startedServices)
		return fmt.Errorf("deploy: commit caddy: %w", err)
	}

	// Confirm the *running* router — not just the admin config tree — actually
	// serves the new deployment before declaring success. Under reload pressure
	// Caddy's admin API can report the new upstream while the compiled router
	// still dials the old (now-deleted) container, 502ing every fresh request.
	if o.DataPlaneProber != nil {
		if err := waitDataPlaneServing(ctx, o.DataPlaneProber, o.DB, *project, dep, cf, log, out); err != nil {
			fmt.Fprintf(out, "↩️  data-plane verify failed, reverting\n")
			if publicStop == nil {
				revertCaddySwap(context.Background(), log, o.Caddy, o.DB, *project, cf)
			}
			cleanupFailed(startedServices)
			return fmt.Errorf("deploy: %w", err)
		}
	}
	if stableTx != nil && !stableTx.incomingStable {
		if err := stableTx.activate(ctx, o.Docker); err != nil {
			cleanupFailed(startedServices)
			return fmt.Errorf("deploy: withdraw old private address: %w", err)
		}
	}
	fmt.Fprintf(out, "✅ traffic swap verified\n")

	// After-hook: best effort. If the deploy is live, a failed
	// after-hook is a 🚨 alert (not a rollback trigger) — the new
	// containers are already serving requests.
	if err := runAfterHook(ctx, o.Docker, *project, dep, cf, envVars, out, out); err != nil {
		log.Warn("after-hook failed (deployment is live)", "error", err)
		fmt.Fprintf(out, "🚨 after-hook failed (deploy is live anyway): %s\n", err)
	}

	if publicStop != nil {
		// These are proven stopped. No router grace period is useful for a
		// service that explicitly accepted downtime.
		for _, name := range publicStop.old {
			if err := o.Docker.RemoveService(ctx, name); err != nil {
				log.Warn("stopFirst old service cleanup", "name", name, "error", err)
			}
		}
	}

	// POST-SUCCESS — clean up old services. Best effort.
	cleanupOldServices(context.Background(), log, o.Docker, *project, dep, deferPublicWebCleanup(cf), out)

	// Cron reconciliation: register / update / remove project crons
	// declared in the just-cut-over cobaltfile. Best effort; failure
	// here is logged but doesn't fail the deploy (the live web/worker
	// services are already serving traffic).
	if o.CronManager != nil {
		if err := o.CronManager.Reconcile(ctx, *project, dep, cf); err != nil {
			log.Warn("cron reconcile after deploy failed", "error", err)
		}
	}

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
// shortSHA renders a 7-char prefix of a git SHA for log lines.
// Empty/short input passes through unchanged.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	if s == "" {
		return "(unknown)"
	}
	return s
}

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

func deferPublicWebCleanup(cf *cobaltfile.Cobaltfile) bool {
	web, ok := cf.Services["web"]
	return ok && web.Type == cobaltfile.TypeContainer && !web.ExposedInternally && !web.StopFirst
}
