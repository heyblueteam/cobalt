package deploy

import (
	"context"
	"fmt"
	"io"
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
	ReconcileStableService(ctx context.Context, opts docker.ServiceCreateOpts) error
	WaitForServiceHealthy(ctx context.Context, name string, replicas int, timeout time.Duration) error
	RemoveService(ctx context.Context, name string) error
	ListServicesForDeployment(ctx context.Context, projectID int64, deploymentNumber int) ([]docker.ServiceInfo, error)
	ListServicesForProject(ctx context.Context, projectID int64) ([]docker.ServiceInfo, error)
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
	stableWeb bool,
	out io.Writer,
) ([]string, error) {
	var started []string
	for _, b := range built {
		if !runsAsService(b.Service) {
			continue
		}
		if stableWeb && b.Name == "web" {
			fmt.Fprintf(out, "🚀 updating stable public web service\n")
			opts := stablePublicWebServiceOpts(project, dep, b, envVars)
			if err := d.ReconcileStableService(ctx, opts); err != nil {
				return started, fmt.Errorf("deploy: reconcile stable public web: %w", err)
			}
			// A stable service may already serve the last successful deployment.
			// Never add it to rollback cleanup, which would turn a failed update
			// into an outage by deleting that known-good service.
			continue
		}
		fmt.Fprintf(out, "🚀 starting service %s\n", b.Name)
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
	stableWeb bool,
	out io.Writer,
) error {
	first := true
	for _, b := range built {
		if !runsAsService(b.Service) {
			continue
		}
		if stableWeb && b.Name == "web" {
			name := docker.StablePublicWebServiceName(project.ID)
			t0 := time.Now()
			if err := d.WaitForServiceHealthy(ctx, name, replicaCount(b.Service), HealthcheckTimeout); err != nil {
				return fmt.Errorf("deploy: wait healthy stable public web: %w", err)
			}
			fmt.Fprintf(out, "✅ stable public web healthy (%s)\n", time.Since(t0).Round(time.Second))
			continue
		}
		if first {
			fmt.Fprintf(out, "⏳ waiting for healthchecks\n")
			first = false
		}
		name := docker.ServiceName(project.Name, dep.Number, b.Name)
		replicas := replicaCount(b.Service)
		t0 := time.Now()
		if err := d.WaitForServiceHealthy(ctx, name, replicas, HealthcheckTimeout); err != nil {
			return fmt.Errorf("deploy: wait healthy %q: %w", b.Name, err)
		}
		fmt.Fprintf(out, "✅ %s healthy (%s)\n", b.Name, time.Since(t0).Round(time.Second))
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

// replicaCount returns the replica count a new deployment of s should be
// created with. Falls back to 1 when minReplicas is unset, matching
// docker's default and preserving prior behavior.
func replicaCount(s cobaltfile.Service) int {
	if s.MinReplicas > 0 {
		return s.MinReplicas
	}
	return 1
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
//
// Network aliases mirror disco's behavior:
//   - per-deployment network → alias = service name (e.g. `web`), so other
//     services within the same project can reach this one by its short
//     name regardless of deployment number.
//   - cobalt-main → if exposedInternally, alias = `{project}-{service}`
//     (e.g. `redis-redis`) — the stable cross-project hostname that
//     callers like api's `REDIS_HOST` rely on. Otherwise the alias is the
//     service's full deployment-numbered name, which keeps the
//     "every service has an alias on every network" invariant for any
//     future reconciler / inspection tooling.
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
		Networks: []docker.NetworkAttachment{
			{Name: deploymentNetwork, Alias: b.Name},
			{Name: MainNetworkName, Alias: mainNetAlias(project, b, dep)},
		},
		Replicas:    replicaCount(b.Service),
		ExtraParams: docker.SplitParams(b.Service.ExtraSwarmParams),
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

// stablePublicWebServiceOpts creates the one long-lived public web service.
// It intentionally attaches only to cobalt-main: a deployment-specific
// network would be deleted with the generation and would reintroduce the DNS
// failure this service exists to prevent.
func stablePublicWebServiceOpts(
	project store.Project,
	dep store.Deployment,
	b BuiltService,
	envVars map[string]string,
) docker.ServiceCreateOpts {
	opts := serviceCreateOpts(project, dep, b, envVars, "")
	stableName := docker.StablePublicWebServiceName(project.ID)
	opts.Name = stableName
	opts.Networks = []docker.NetworkAttachment{{Name: MainNetworkName, Alias: stableName}}
	return opts
}

// mainNetAlias returns the DNS alias a service should answer to on the
// shared cobalt-main overlay. When the service opts into cross-project
// reachability via `exposedInternally`, the alias is the stable short form
// `{project}-{service}` — what env vars like `REDIS_HOST=redis-redis`
// resolve against. Otherwise the alias is the service's full
// deployment-numbered name, keeping the alias field non-empty so any
// future tooling that reads `docker service inspect ... .Aliases` sees a
// uniform shape across all cobalt-managed services.
func mainNetAlias(project store.Project, b BuiltService, dep store.Deployment) string {
	if b.Service.ExposedInternally {
		return project.Name + "-" + b.Name
	}
	return docker.ServiceName(project.Name, dep.Number, b.Name)
}
