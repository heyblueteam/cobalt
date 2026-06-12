package deploy

import (
	"context"
	"fmt"
	"io"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// stopFirstPhase removes the OLD generations of every stopFirst service
// before the new generation is created. This is the escape hatch for
// services that publish host-mode ports: two generations can't bind the
// same host port, so the default start-first ordering deadlocks until the
// health timeout. The price is a downtime window from here until the new
// service passes its healthcheck — stopFirst services opted into that.
//
// Errors are fatal (unlike cleanupOldServices' best-effort): if the old
// service can't be removed, the new one will deadlock on the port anyway,
// and failing here surfaces the real cause instead of a 5-minute timeout.
func stopFirstPhase(
	ctx context.Context,
	d ServiceDocker,
	project store.Project,
	dep store.Deployment,
	built []BuiltService,
	out io.Writer,
) error {
	var flagged []string
	for _, b := range built {
		if b.Service.StopFirst && runsAsService(b.Service) {
			flagged = append(flagged, b.Name)
		}
	}
	if len(flagged) == 0 {
		return nil
	}

	services, err := d.ListServicesForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("deploy: stopFirst: list services: %w", err)
	}

	for _, name := range flagged {
		for _, s := range services {
			// Labels, not name parsing: ServiceName/DeploymentNumber come from
			// the service's own cobalt labels, so a `smtp` flag can never match
			// an unrelated `old-smtp` service.
			if s.ServiceName != name || s.DeploymentNumber == dep.Number {
				continue
			}
			fmt.Fprintf(out, "⏹  stopping %s before start (stopFirst — service is down until #%d is healthy)\n",
				s.Name, dep.Number)
			if err := d.RemoveService(ctx, s.Name); err != nil {
				return fmt.Errorf("deploy: stopFirst: remove %q: %w", s.Name, err)
			}
		}
	}
	return nil
}

// Compile-time check that the real docker client satisfies the widened
// ServiceDocker interface (ListServicesForProject is also used by cleanup).
var _ ServiceDocker = (*docker.Client)(nil)
