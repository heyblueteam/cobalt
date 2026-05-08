package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// ErrRollbackImageMissing is returned by EnqueueRollback / rollbackRun
// when the target deployment's cached image is no longer present on
// disk — the operator must redeploy the commit instead. Mapped to a
// 410 Gone at the API layer.
var ErrRollbackImageMissing = errors.New("rollback: target image no longer cached")

// ErrRollbackTargetIsCurrent is returned when the requested rollback
// target is already the live deployment. Mapped to 409 Conflict.
var ErrRollbackTargetIsCurrent = errors.New("rollback: target is the current live deployment")

// ErrRollbackNoPrior is returned when the default form
// (`cobalt rollback <project>` with no --to) finds no successful
// deployment to roll back to.
var ErrRollbackNoPrior = errors.New("rollback: no prior successful deployment to roll back to")

// rollbackRun executes the cutover phase against the target
// deployment's cached image. Called from Orchestrator.Run when the new
// row's RollbackOf is set. Assumes the target's resolved_cobaltfile
// has already been copied onto the new row by the API handler — that
// way the cutover writes Caddy/swarm against the new row's number but
// uses the historical service shape.
func (o *Orchestrator) rollbackRun(
	ctx context.Context,
	log *slog.Logger,
	project *store.Project,
	dep store.Deployment,
	envVars map[string]string,
	out io.Writer,
) error {
	if dep.RollbackOf == nil {
		return errors.New("rollback: RollbackOf must be set")
	}
	target, err := o.DB.GetDeployment(ctx, *dep.RollbackOf)
	if err != nil {
		return fmt.Errorf("rollback: load target: %w", err)
	}
	if target.ResolvedCobaltfile == nil {
		return fmt.Errorf("rollback: deployment #%d has no recorded cobaltfile", target.Number)
	}
	cf, err := cobaltfile.Parse([]byte(*target.ResolvedCobaltfile))
	if err != nil {
		return fmt.Errorf("rollback: parse target cobaltfile: %w", err)
	}

	built, err := reconstructBuiltServicesFromTarget(ctx, o, project.Name, target.Number, cf)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "==> rolling back to deployment #%d (commit %s)\n",
		target.Number, commitDisplay(target.CommitSHA))
	fmt.Fprintf(out, "==> reusing %d cached image(s)\n", countImageBuilds(built))

	// Persist the cobaltfile on the new row so subsequent reads (e.g.
	// caddy reconciler, scale list) see the rollback's effective shape.
	if target.ResolvedCobaltfile != nil {
		if err := o.DB.SetResolvedCobaltfile(ctx, dep.ID, *target.ResolvedCobaltfile); err != nil {
			log.Warn("rollback: persist resolved cobaltfile", "error", err)
		}
	}

	return o.cutover(ctx, log, project, dep, built, cf, envVars, out)
}

// reconstructBuiltServicesFromTarget rebuilds the []BuiltService list
// that startServicesPhase expects, using the target deployment's
// cobaltfile and image-tag convention (cobalt/project-{name}-{image}:
// {targetNumber}). Verifies each referenced image is still cached
// before returning — refusing here is much better than a half-started
// rollback that fails healthcheck.
func reconstructBuiltServicesFromTarget(
	ctx context.Context,
	o *Orchestrator,
	projectName string,
	targetNumber int,
	cf *cobaltfile.Cobaltfile,
) ([]BuiltService, error) {
	if len(cf.Services) == 0 {
		return nil, fmt.Errorf("rollback: target cobaltfile has no services")
	}

	// Probe every distinct image tag once; a service with image="" or
	// a non-container type doesn't need an image and gets ImageTag="".
	imageTagCache := map[string]string{}
	for _, svc := range cf.Services {
		if svc.Type != "" && svc.Type != cobaltfile.TypeContainer {
			continue
		}
		image := svc.Image
		if image == "" {
			image = "default"
		}
		if _, seen := imageTagCache[image]; seen {
			continue
		}
		tag := docker.InternalImageName(projectName, image, targetNumber)
		exists, err := o.Docker.ImageExists(ctx, tag)
		if err != nil {
			return nil, fmt.Errorf("rollback: probe image %s: %w", tag, err)
		}
		if !exists {
			return nil, fmt.Errorf("%w: %s (run `cobalt deploy --commit <sha>` to rebuild)",
				ErrRollbackImageMissing, tag)
		}
		imageTagCache[image] = tag
	}

	built := make([]BuiltService, 0, len(cf.Services))
	for name, svc := range cf.Services {
		tag := ""
		if svc.Type == "" || svc.Type == cobaltfile.TypeContainer {
			image := svc.Image
			if image == "" {
				image = "default"
			}
			tag = imageTagCache[image]
		}
		built = append(built, BuiltService{
			Name:     name,
			Service:  svc,
			ImageTag: tag,
		})
	}
	return built, nil
}

func countImageBuilds(b []BuiltService) int {
	seen := map[string]struct{}{}
	for _, s := range b {
		if s.ImageTag != "" {
			seen[s.ImageTag] = struct{}{}
		}
	}
	return len(seen)
}

func commitDisplay(s *string) string {
	if s == nil || *s == "" {
		return "(unknown)"
	}
	if len(*s) > 7 {
		return (*s)[:7]
	}
	return *s
}
