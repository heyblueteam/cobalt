package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// HealthcheckTimeout is how long startServicesPhase waits for each new
// service to reach healthy / running before failing the deploy.
const HealthcheckTimeout = 5 * time.Minute

// ServiceDocker is the docker subset the service phase uses.
type ServiceDocker interface {
	CreateService(ctx context.Context, opts docker.ServiceCreateOpts) error
	WaitForServiceHealthy(ctx context.Context, name string, replicas int, timeout time.Duration) error
	RemoveService(ctx context.Context, name string) error
	ListServicesForDeployment(ctx context.Context, projectID int64, deploymentNumber int) ([]docker.ServiceInfo, error)
}

// startServicesPhase creates and starts every long-running service in the
// cobaltfile (container, static-with-command, generator). Returns the list
// of service names that were started so callers can clean them up if a
// later phase fails.
//
// On any per-service error, services already started are NOT touched —
// the orchestrator's defer cleanup is responsible for that.
func startServicesPhase(
	ctx context.Context,
	d ServiceDocker,
	project store.Project,
	dep store.Deployment,
	built []BuiltService,
	envVars map[string]string,
	deploymentNetwork string,
) ([]string, error) {
	var started []string
	for _, b := range built {
		if !runsAsService(b.Service) {
			continue
		}
		opts := serviceCreateOpts(project, dep, b, envVars, deploymentNetwork)
		if err := d.CreateService(ctx, opts); err != nil {
			return started, fmt.Errorf("deploy: create service %q: %w", b.Name, err)
		}
		started = append(started, docker.ServiceName(project.Name, dep.Number, b.Name))
	}
	return started, nil
}

// waitHealthyAll blocks until every service reaches healthy/running, or
// the supplied per-service timeout elapses, or ctx is canceled.
func waitHealthyAll(
	ctx context.Context,
	d ServiceDocker,
	project store.Project,
	dep store.Deployment,
	built []BuiltService,
) error {
	for _, b := range built {
		if !runsAsService(b.Service) {
			continue
		}
		name := docker.ServiceName(project.Name, dep.Number, b.Name)
		replicas := 1 // cobaltfile services don't expose replica count yet; default 1
		if err := d.WaitForServiceHealthy(ctx, name, replicas, HealthcheckTimeout); err != nil {
			return fmt.Errorf("deploy: wait healthy %q: %w", b.Name, err)
		}
	}
	return nil
}

// stopServices removes the supplied set of service names. Errors are
// returned only for the *first* failure — callers usually want best-
// effort cleanup, so most call sites ignore the return.
func stopServices(ctx context.Context, d ServiceDocker, names []string) error {
	var firstErr error
	for _, n := range names {
		if err := d.RemoveService(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// runsAsService reports whether a cobaltfile service should be started as
// a long-running swarm service (vs. one-shot container hooks, crons, or
// generator).
func runsAsService(s cobaltfile.Service) bool {
	switch s.Type {
	case cobaltfile.TypeContainer:
		return true
	case cobaltfile.TypeStatic:
		// Static sites are served by Caddy directly; no swarm service.
		return false
	case cobaltfile.TypeGenerator, cobaltfile.TypeCommand, cobaltfile.TypeCron:
		return false
	}
	return false
}

// serviceCreateOpts translates a BuiltService into the docker package's
// ServiceCreateOpts, attaching the per-deployment network plus cobalt-main
// so cross-service hooks and exposedInternally references resolve.
func serviceCreateOpts(
	project store.Project,
	dep store.Deployment,
	b BuiltService,
	envVars map[string]string,
	deploymentNetwork string,
) docker.ServiceCreateOpts {
	opts := docker.ServiceCreateOpts{
		ProjectID:        project.ID,
		ProjectName:      project.Name,
		ServiceName:      b.Name,
		DeploymentNumber: dep.Number,
		Image:            b.ImageTag,
		Command:          b.Service.Command,
		EnvVars:          envVars,
		Networks:         []string{deploymentNetwork, MainNetworkName},
		Replicas:         1,
		ExtraParams:      docker.SplitParams(b.Service.ExtraSwarmParams),
	}
	if b.Service.Health != nil && b.Service.Health.Command != "" {
		opts.HealthCommand = b.Service.Health.Command
	}
	for _, p := range b.Service.PublishedPorts {
		opts.PublishedPorts = append(opts.PublishedPorts, docker.PublishedPort{
			PublishedAs:       p.PublishedAs,
			FromContainerPort: p.FromContainerPort,
			Protocol:          p.Protocol,
		})
	}
	for _, v := range b.Service.Volumes {
		opts.Volumes = append(opts.Volumes, docker.ServiceVolume{
			VolumeName:      docker.VolumeName(project.ID, v.Name),
			DestinationPath: v.DestinationPath,
		})
	}
	return opts
}
