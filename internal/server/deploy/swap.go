package deploy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// SwapDocker is the docker subset swap-step helpers need (currently nothing
// — swap goes through Caddy. Defined for symmetry).
type SwapDocker interface{}

// SwapCaddy is the Caddy subset swap-step helpers need.
type SwapCaddy interface {
	SetDomainsForProject(ctx context.Context, projectID int64, domains []string) error
	VerifyServeService(ctx context.Context, projectID int64, container string, port, deploymentNumber int) error
	ServeStaticSite(ctx context.Context, projectID int64, projectName string, deploymentNumber int) error
	ServeService(ctx context.Context, projectID int64, container string, port, deploymentNumber int) error
	AddProjectRoute(ctx context.Context, projectID int64, domains []string) error
}

// SwapStore is the store subset swap-step helpers need.
type SwapStore interface {
	// ListPrimaryDomainsForProject returns just the primary (non-
	// redirect) hosts. The deploy swap uses this when building the
	// project route's host matcher; redirect hosts are owned by
	// separate Caddy routes (managed by the API domain handlers) and
	// must not be folded into the reverse-proxy matcher.
	ListPrimaryDomainsForProject(ctx context.Context, projectID int64) ([]string, error)
	GetLastSuccessfulDeployment(ctx context.Context, projectID int64) (*store.Deployment, error)
}

// commitCaddySwap performs the Caddy cutover for a project.
//
// Short-circuits when:
//   - The cobaltfile has no `web` service (only-crons project), OR
//   - The `web` service is exposedInternally=true — reached by other
//     cobalt projects via cobalt-main DNS alias, never via Caddy. There
//     is no project route to PATCH; trying anyway would fail with
//     `unknown object ID cobalt-project-handler-N`.
//
// Otherwise:
//   - web is type=container: VerifyServeService swaps + verifies upstream.
//   - web is type=static or generator: ServeStaticSite swaps Caddy to
//     file_server. (Static-site verification is best-effort: we just
//     verify the route exists; we don't deeply check the file_server
//     config.)
//
// Route recovery: SetDomainsForProject above confirms/creates the route
// once, up front — but Caddy can lose the entire route (and every @id
// inside it, including the handler the switch below PATCHes) *after* that
// check and *before* the PATCH: the admin-API watchdog self-heals an
// unresponsive Caddy by restarting it, which boots from an empty bootstrap
// config until the next caddy-reconcile tick repopulates routes, and the
// reconciler explicitly stands down for a project with an in-flight deploy
// (this one) — so nothing else will recreate it. Root-caused from the
// 2026-07-20 blue.app incident: deploy 388's handler PATCH landed 4.3s after
// Caddy rebooted, seconds before the next reconcile tick, which skipped
// project 1 anyway because this deploy was "active". If the handler PATCH
// 404s with "unknown object ID", that's indistinguishable from "the route
// vanished out from under us" — recreate it once via the same idempotent
// AddProjectRoute the reconciler uses, then retry exactly once.
func commitCaddySwap(
	ctx context.Context,
	cy SwapCaddy,
	st SwapStore,
	project store.Project,
	dep store.Deployment,
	cf *cobaltfile.Cobaltfile,
) error {
	web, ok := cf.Services["web"]
	if !ok || web.ExposedInternally {
		return nil
	}

	domains, err := st.ListPrimaryDomainsForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("deploy.commitCaddySwap: list domains: %w", err)
	}
	if err := cy.SetDomainsForProject(ctx, project.ID, domains); err != nil {
		return fmt.Errorf("deploy.commitCaddySwap: set domains: %w", err)
	}

	switch web.Type {
	case cobaltfile.TypeContainer:
		container := docker.ServiceName(project.Name, dep.Number, "web")
		if cf.UsesStablePublicWeb() {
			container = docker.StablePublicWebServiceName(project.ID)
		}
		port := web.Port
		err := cy.VerifyServeService(ctx, project.ID, container, port, dep.Number)
		if caddy.IsNotFound(err) {
			err = recreateRouteThenRetry(ctx, cy, project.ID, domains, func() error {
				return cy.VerifyServeService(ctx, project.ID, container, port, dep.Number)
			})
		}
		if err != nil {
			return fmt.Errorf("deploy.commitCaddySwap: %w", err)
		}
	case cobaltfile.TypeStatic, cobaltfile.TypeGenerator:
		err := cy.ServeStaticSite(ctx, project.ID, project.Name, dep.Number)
		if caddy.IsNotFound(err) {
			err = recreateRouteThenRetry(ctx, cy, project.ID, domains, func() error {
				return cy.ServeStaticSite(ctx, project.ID, project.Name, dep.Number)
			})
		}
		if err != nil {
			return fmt.Errorf("deploy.commitCaddySwap: serve static: %w", err)
		}
	default:
		return fmt.Errorf("deploy.commitCaddySwap: unsupported web type %q", web.Type)
	}
	return nil
}

// recreateRouteThenRetry recreates a project's Caddy route (AddProjectRoute
// is idempotent — safe even if the route turns out to still exist, e.g. a
// 404 from something other than route-loss) and runs op again. Used when a
// handler PATCH 404s with "unknown object ID", which happens when Caddy's
// admin-API watchdog restarts Caddy between commitCaddySwap's earlier route
// check and this PATCH — see commitCaddySwap's doc comment.
func recreateRouteThenRetry(ctx context.Context, cy SwapCaddy, projectID int64, domains []string, op func() error) error {
	if err := cy.AddProjectRoute(ctx, projectID, domains); err != nil {
		return fmt.Errorf("recreate route after missing handler: %w", err)
	}
	return op()
}

// revertCaddySwap is the Phase 2 failure path. It looks up the last
// successful deployment and PATCHes Caddy back to its web service.
//
// Best effort: if no prior success exists (first deploy of a project),
// this is a no-op. If the revert PATCH itself fails, we log loudly so the
// 8d convergence loop can pick up the drift.
func revertCaddySwap(
	ctx context.Context,
	log *slog.Logger,
	cy SwapCaddy,
	st SwapStore,
	project store.Project,
	cf *cobaltfile.Cobaltfile,
) {
	prev, err := st.GetLastSuccessfulDeployment(ctx, project.ID)
	if err != nil {
		// ErrNotFound is expected for a project's first deploy.
		log.Info("deploy.revertCaddySwap: no prior success to revert to",
			"project_id", project.ID, "error", err)
		return
	}
	web, ok := cf.Services["web"]
	if !ok || web.ExposedInternally {
		// No public Caddy route for this project — nothing to revert.
		// Mirrors the commitCaddySwap carve-out: exposedInternally web
		// services have no Caddy state to undo.
		return
	}
	switch web.Type {
	case cobaltfile.TypeContainer:
		container := docker.ServiceName(project.Name, prev.Number, "web")
		if cf.UsesStablePublicWeb() {
			container = docker.StablePublicWebServiceName(project.ID)
		}
		if err := cy.ServeService(ctx, project.ID, container, web.Port, prev.Number); err != nil {
			log.Error("deploy.revertCaddySwap: container revert failed",
				"project_id", project.ID, "want_upstream", container, "error", err)
		}
	case cobaltfile.TypeStatic, cobaltfile.TypeGenerator:
		if err := cy.ServeStaticSite(ctx, project.ID, project.Name, prev.Number); err != nil {
			log.Error("deploy.revertCaddySwap: static revert failed",
				"project_id", project.ID, "deployment", prev.Number, "error", err)
		}
	}
}
