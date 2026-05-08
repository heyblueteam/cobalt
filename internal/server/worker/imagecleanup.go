package worker

import (
	"context"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// ProjectLister returns every cobalt-managed project. Implemented by
// *store.DB; defined as an interface here so tests can substitute a fake.
type ProjectLister interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
}

// DeploymentNumberLister returns the deployment numbers that should
// retain their docker images for a given project (i.e., still queued /
// running / serving). store.DB.ActiveDeploymentNumbers satisfies this.
type DeploymentNumberLister interface {
	ActiveDeploymentNumbers(ctx context.Context, projectID int64) ([]int, error)
	// RecentSuccessfulDeploymentNumbers returns the latest `limit`
	// successful deployment numbers for rollback retention. limit <= 0
	// disables the rollback window.
	RecentSuccessfulDeploymentNumbers(ctx context.Context, projectID int64, limit int) ([]int, error)
}

// ImageRemover is the docker operation the cleanup job uses. We define
// the smallest possible interface so tests can fake just this method.
type ImageRemover interface {
	ListInternalImages(ctx context.Context, projectName string) ([]docker.ImageInfo, error)
	RemoveImage(ctx context.Context, tag string) error
}

// CleanupImages prunes internal images whose deployment number is no
// longer active for any project AND falls outside the rollback
// retention window. Failures on individual images are logged and
// skipped — one stuck image must not stop the whole sweep.
//
// rollbackRetention is the number of recent successful deployments
// per project whose images we keep on disk so `cobalt rollback`
// can target them. Pass 0 to keep only active deploys.
//
// Returns the count of images successfully removed.
func CleanupImages(
	ctx context.Context,
	log *slog.Logger,
	projects ProjectLister,
	deploys DeploymentNumberLister,
	docker ImageRemover,
	rollbackRetention int,
) (int, error) {
	allProjects, err := projects.ListProjects(ctx)
	if err != nil {
		return 0, err
	}

	var removed int
	for _, p := range allProjects {
		active, err := deploys.ActiveDeploymentNumbers(ctx, p.ID)
		if err != nil {
			log.Error("imagecleanup: list active deploys",
				"project_id", p.ID, "project", p.Name, "error", err)
			continue
		}
		recent, err := deploys.RecentSuccessfulDeploymentNumbers(ctx, p.ID, rollbackRetention)
		if err != nil {
			log.Error("imagecleanup: recent successful",
				"project_id", p.ID, "project", p.Name, "error", err)
			continue
		}
		activeSet := make(map[int]struct{}, len(active)+len(recent))
		for _, n := range active {
			activeSet[n] = struct{}{}
		}
		for _, n := range recent {
			activeSet[n] = struct{}{}
		}

		images, err := docker.ListInternalImages(ctx, p.Name)
		if err != nil {
			log.Error("imagecleanup: list images",
				"project_id", p.ID, "project", p.Name, "error", err)
			continue
		}

		for _, img := range images {
			if _, keep := activeSet[img.DeploymentNumber]; keep {
				continue
			}
			if err := docker.RemoveImage(ctx, img.Tag); err != nil {
				log.Warn("imagecleanup: remove failed",
					"tag", img.Tag, "project_id", p.ID, "error", err)
				continue
			}
			removed++
			log.Info("imagecleanup: removed",
				"tag", img.Tag, "project_id", p.ID, "deployment", img.DeploymentNumber)
		}
	}
	log.Info("imagecleanup: sweep complete", "removed", removed, "projects", len(allProjects))
	return removed, nil
}
