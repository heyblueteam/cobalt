package worker

import (
	"context"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// NetworkOps is the smallest docker surface CleanupNetworks needs.
// Defined as an interface so tests can substitute a fake without
// reaching into the docker package's runner.
type NetworkOps interface {
	ListNetworksForProject(ctx context.Context, projectID int64, projectName string) ([]docker.NetworkInfo, error)
	RemoveNetwork(ctx context.Context, name string) error
}

// CleanupNetworks prunes overlay networks whose deployment number is no
// longer active for any project. Mirrors CleanupImages: per-project errors
// are logged and skipped — a single stuck project must not stop the sweep.
//
// Safety properties (vs upstream disco's "never remove" stance):
//
//   - Filtering by cobalt.project.id label means we never touch unlabeled
//     networks (host bridges, ingress, disco leftovers, hand-created nets).
//   - Cobalt names networks with their deployment number. We never re-create
//     with the same name, so moby/moby#37338's IP-leak path is not exercised.
//   - Docker refuses `network rm` if any endpoint is still attached. That's
//     our race protection if a deploy is mid-flight at sweep time.
//
// Returns the count of networks successfully removed.
func CleanupNetworks(
	ctx context.Context,
	log *slog.Logger,
	projects ProjectLister,
	deploys DeploymentNumberLister,
	docker NetworkOps,
) (int, error) {
	allProjects, err := projects.ListProjects(ctx)
	if err != nil {
		return 0, err
	}

	var removed int
	for _, p := range allProjects {
		removed += cleanupProjectNetworks(ctx, log, p, deploys, docker)
	}
	log.Info("networkcleanup: sweep complete", "removed", removed, "projects", len(allProjects))
	return removed, nil
}

func cleanupProjectNetworks(
	ctx context.Context,
	log *slog.Logger,
	p store.Project,
	deploys DeploymentNumberLister,
	dockerOps NetworkOps,
) int {
	active, err := deploys.ActiveDeploymentNumbers(ctx, p.ID)
	if err != nil {
		log.Error("networkcleanup: list active deploys",
			"project_id", p.ID, "project", p.Name, "error", err)
		return 0
	}
	activeSet := make(map[int]struct{}, len(active))
	for _, n := range active {
		activeSet[n] = struct{}{}
	}

	nets, err := dockerOps.ListNetworksForProject(ctx, p.ID, p.Name)
	if err != nil {
		log.Error("networkcleanup: list networks",
			"project_id", p.ID, "project", p.Name, "error", err)
		return 0
	}

	var removed int
	for _, n := range nets {
		if _, keep := activeSet[n.DeploymentNumber]; keep {
			continue
		}
		if err := dockerOps.RemoveNetwork(ctx, n.Name); err != nil {
			// Most common case here is "network has active endpoints" —
			// a deploy raced us. Log at warn (not error) and move on;
			// next sweep will catch it.
			log.Warn("networkcleanup: remove failed",
				"network", n.Name, "project_id", p.ID, "error", err)
			continue
		}
		removed++
		log.Info("networkcleanup: removed",
			"network", n.Name, "project_id", p.ID, "deployment", n.DeploymentNumber)
	}
	return removed
}
