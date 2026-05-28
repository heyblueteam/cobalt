package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// CaddyReconcileStore is the store subset the Caddy reconciler needs.
type CaddyReconcileStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	GetLastSuccessfulDeployment(ctx context.Context, projectID int64) (*store.Deployment, error)
	// ListPrimaryDomainsForProject returns the project's primary
	// (non-redirect) hosts. The convergence reconciler only manages the
	// project's reverse-proxy host matchers; redirect routes are owned
	// by separate Caddy `cobalt-redirect-*` routes and reconciled out
	// of band by the API domain handlers.
	ListPrimaryDomainsForProject(ctx context.Context, projectID int64) ([]string, error)
	// ActiveDeploymentForProject returns the project's in-flight deployment
	// (fetching/building/swapping), or store.ErrNotFound if none. The
	// reconciler uses it to stand down while a deploy owns the project's
	// Caddy state — see reconcileProject.
	ActiveDeploymentForProject(ctx context.Context, projectID int64) (*store.Deployment, error)
}

// CaddyReconcileTarget is the Caddy subset the reconciler talks to.
type CaddyReconcileTarget interface {
	ProjectRouteExists(ctx context.Context, projectID int64) (bool, error)
	AddProjectRoute(ctx context.Context, projectID int64, domains []string) error
	CurrentUpstream(ctx context.Context, projectID int64) (string, error)
	CurrentDomains(ctx context.Context, projectID int64) ([]string, error)
	ServeService(ctx context.Context, projectID int64, container string, port int) error
	ServeStaticSite(ctx context.Context, projectID int64, projectName string, deploymentNumber int) error
	SetDomainsForProject(ctx context.Context, projectID int64, domains []string) error
}

// reconcileResult tallies per-cycle work for tests + observability.
type reconcileResult struct {
	Examined  int // projects checked
	Corrected int // projects whose Caddy state was repaired
}

// ReconcileCaddyState walks every project that has a last-successful
// deployment, derives its desired Caddy state from the deployment's
// resolved cobaltfile, and PATCHes Caddy back to that state where it has
// drifted. Implements improvement B from the deploy-flow audit (root fix
// for upstream issue #97).
//
// Each project's reconciliation is independent — failures are logged and
// don't halt the sweep.
//
// Returns the number of corrections issued. Errors from the project
// listing query are fatal; everything else is logged and absorbed.
func ReconcileCaddyState(
	ctx context.Context,
	log *slog.Logger,
	st CaddyReconcileStore,
	cy CaddyReconcileTarget,
) (int, error) {
	if log == nil {
		log = slog.Default()
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return 0, fmt.Errorf("caddy reconcile: list projects: %w", err)
	}

	r := reconcileResult{}
	for _, p := range projects {
		corrected, err := reconcileProject(ctx, log, st, cy, p)
		if err != nil {
			log.Warn("caddy reconcile: project failed",
				"project_id", p.ID, "project", p.Name, "error", err)
			continue
		}
		r.Examined++
		if corrected {
			r.Corrected++
		}
	}
	if r.Corrected > 0 {
		log.Info("caddy reconcile: corrections applied",
			"examined", r.Examined, "corrected", r.Corrected)
	}
	return r.Corrected, nil
}

// reconcileProject is the per-project worker. Returns true if a
// correction was applied, false if no action was needed.
func reconcileProject(
	ctx context.Context,
	log *slog.Logger,
	st CaddyReconcileStore,
	cy CaddyReconcileTarget,
	p store.Project,
) (bool, error) {
	// Stand down while a deploy is in flight. A deploy owns its project's
	// Caddy state end-to-end (start services → swap upstream → verify →
	// mark success). The reconciler's notion of "desired" is the LAST
	// SUCCESSFUL deployment, which is stale during a deploy: acting on it
	// here PATCHes the upstream back to the previous container while the
	// deploy is swapping to the new one, so the deploy's verify reads the
	// reverted value and fails ("serve verify drifted"). Skip until the
	// deploy reaches a terminal state; the post-deploy state is then
	// authoritative and we reconcile against it normally.
	if _, err := st.ActiveDeploymentForProject(ctx, p.ID); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("check active deployment: %w", err)
	}

	live, err := st.GetLastSuccessfulDeployment(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil // never deployed; nothing to reconcile
	}
	if err != nil {
		return false, fmt.Errorf("get last success: %w", err)
	}
	if live.ResolvedCobaltfile == nil {
		return false, nil
	}

	cf, err := cobaltfile.Parse([]byte(*live.ResolvedCobaltfile))
	if err != nil {
		return false, fmt.Errorf("parse stored cobaltfile: %w", err)
	}
	web, ok := cf.Services["web"]
	if !ok {
		// Project has no public-facing web service — Caddy shouldn't
		// have a route either. Don't enforce here; project deletion
		// handles route removal.
		return false, nil
	}

	domains, err := st.ListPrimaryDomainsForProject(ctx, p.ID)
	if err != nil {
		return false, fmt.Errorf("list domains: %w", err)
	}
	if len(domains) == 0 {
		// No domains → Caddy has nowhere to route. Skip.
		return false, nil
	}

	exists, err := cy.ProjectRouteExists(ctx, p.ID)
	if err != nil {
		return false, fmt.Errorf("probe route: %w", err)
	}
	if !exists {
		log.Warn("caddy reconcile: route missing, recreating",
			"project_id", p.ID, "project", p.Name)
		if err := cy.AddProjectRoute(ctx, p.ID, domains); err != nil {
			return false, fmt.Errorf("recreate route: %w", err)
		}
		return reapplyHandler(ctx, cy, p, *live, web)
	}

	// Domains may have drifted since last deploy — but only PATCH when they
	// actually differ. Every admin PATCH triggers a full Caddy config reload
	// (8-12s on a busy server, re-provisioning all TLS-managed domains), so
	// re-asserting an unchanged host list for every project every cycle is
	// pure load on the admin endpoint and a prior contributor to its
	// saturation. Read the live host set and skip the no-op case.
	liveDomains, err := cy.CurrentDomains(ctx, p.ID)
	if err != nil {
		return false, fmt.Errorf("read current domains: %w", err)
	}
	if !sameHostSet(liveDomains, domains) {
		if err := cy.SetDomainsForProject(ctx, p.ID, domains); err != nil {
			return false, fmt.Errorf("set domains: %w", err)
		}
	}

	switch web.Type {
	case cobaltfile.TypeContainer:
		want := docker.ServiceName(p.Name, live.Number, "web")
		got, err := cy.CurrentUpstream(ctx, p.ID)
		if err != nil {
			return false, fmt.Errorf("read current upstream: %w", err)
		}
		if got == want {
			return false, nil
		}
		log.Warn("caddy reconcile: upstream drifted",
			"project_id", p.ID, "project", p.Name,
			"want", want, "got", got)
		if err := cy.ServeService(ctx, p.ID, want, web.Port); err != nil {
			return false, fmt.Errorf("repair upstream: %w", err)
		}
		return true, nil

	case cobaltfile.TypeStatic, cobaltfile.TypeGenerator:
		// We don't have a cheap "what is the current root?" probe (it
		// would need a deeper GET on the file_server config). For v1 we
		// only repair the route-missing case (above) for static sites.
		return false, nil
	}
	return false, nil
}

// reapplyHandler re-PATCHes the right handler kind onto a freshly
// recreated route. Called after AddProjectRoute installs a placeholder.
func reapplyHandler(
	ctx context.Context,
	cy CaddyReconcileTarget,
	p store.Project,
	live store.Deployment,
	web cobaltfile.Service,
) (bool, error) {
	switch web.Type {
	case cobaltfile.TypeContainer:
		container := docker.ServiceName(p.Name, live.Number, "web")
		if err := cy.ServeService(ctx, p.ID, container, web.Port); err != nil {
			return false, fmt.Errorf("reapply container: %w", err)
		}
	case cobaltfile.TypeStatic, cobaltfile.TypeGenerator:
		if err := cy.ServeStaticSite(ctx, p.ID, p.Name, live.Number); err != nil {
			return false, fmt.Errorf("reapply static: %w", err)
		}
	default:
		return false, fmt.Errorf("unsupported web type %q", web.Type)
	}
	return true, nil
}

// sameHostSet reports whether a and b contain the same hosts, ignoring
// order. Caddy's host matcher is order-independent, so a reordered list is
// not drift and must not trigger a reload.
func sameHostSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, h := range a {
		counts[h]++
	}
	for _, h := range b {
		counts[h]--
		if counts[h] < 0 {
			return false
		}
	}
	return true
}

// Compile-time interface satisfaction checks. Production wiring uses
// *store.DB and *caddy.Client.
var (
	_ CaddyReconcileStore  = (*store.DB)(nil)
	_ CaddyReconcileTarget = (*caddy.Client)(nil)
)
