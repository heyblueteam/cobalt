package worker

import (
	"context"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/docker"
)

// ProjectLister returns every cobalt-managed project. Implemented by
// store.DB; defined as an interface here to keep the worker package
// loosely coupled and easily testable.
type ProjectLister interface {
	ListProjects(ctx context.Context) ([]Project, error)
}

// DeploymentNumberLister returns the deployment numbers that should
// retain their docker images for a given project (i.e., still queued /
// running / serving). store.DB.ActiveDeploymentNumbers satisfies this.
type DeploymentNumberLister interface {
	ActiveDeploymentNumbers(ctx context.Context, projectID int64) ([]int, error)
}

// Project is the worker's local view of a project — just enough fields to
// build image tags and labels. Mirrors store.Project's relevant fields so
// the worker doesn't import store directly (avoids a tight coupling that
// makes mocking harder).
type Project struct {
	ID   int64
	Name string
}

// ImageRemover is the docker operation the cleanup job uses. We define
// the smallest possible interface so tests can fake just this method.
type ImageRemover interface {
	ListInternalImages(ctx context.Context, projectName string) ([]docker.ImageInfo, error)
	RemoveImage(ctx context.Context, tag string) error
}

// CleanupImages prunes internal images whose deployment number is no
// longer active for any project. Failures on individual images are logged
// and skipped — one stuck image must not stop the whole sweep.
//
// Returns the count of images successfully removed.
func CleanupImages(
	ctx context.Context,
	log *slog.Logger,
	projects ProjectLister,
	deploys DeploymentNumberLister,
	docker ImageRemover,
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
		activeSet := make(map[int]struct{}, len(active))
		for _, n := range active {
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
