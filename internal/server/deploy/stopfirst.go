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
		if b.Service.StopFirst && runsAsService(b.Service) && (b.Name != "web" || b.Service.ExposedInternally) {
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
			// `docker service ls` gives us names only (listServices fills just
			// Name/Replicas — the label-derived ServiceInfo fields are NOT
			// populated on this path), so match by exact name parse. Generation
			// treats project and service as known, so a `smtp` flag can never
			// match an unrelated `old-smtp` service.
			gen, ok := docker.Generation(project.Name, name, s.Name)
			if !ok || gen == dep.Number {
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

type portPreflightDocker interface {
	ListServicesForProject(context.Context, int64) ([]docker.ServiceInfo, error)
	ServicePublishedPorts(context.Context, string) ([]docker.PublishedPort, error)
}

// publishedPortsPreflight fails a deploy before anything changes when a
// start-first service publishes a port that an older generation still holds.
// Swarm would reject the new service at create time ("port already in use as
// an ingress port"), after stopFirst siblings were already removed, leaving the
// old generation running without its data services.
func publishedPortsPreflight(
	ctx context.Context,
	d portPreflightDocker,
	project store.Project,
	dep store.Deployment,
	built []BuiltService,
) error {
	var startFirst []BuiltService
	for _, b := range built {
		if runsAsService(b.Service) && !b.Service.StopFirst && len(b.Service.PublishedPorts) > 0 {
			startFirst = append(startFirst, b)
		}
	}
	if len(startFirst) == 0 {
		return nil
	}
	services, err := d.ListServicesForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("deploy: published ports preflight: list services: %w", err)
	}
	for _, b := range startFirst {
		for _, s := range services {
			gen, ok := docker.Generation(project.Name, b.Name, s.Name)
			if !ok || gen == dep.Number {
				continue
			}
			held, err := d.ServicePublishedPorts(ctx, s.Name)
			if err != nil {
				return fmt.Errorf("deploy: published ports preflight: inspect %q: %w", s.Name, err)
			}
			for _, want := range b.Service.PublishedPorts {
				for _, h := range held {
					if h.PublishedAs == want.PublishedAs && protocolOrTCP(h.Protocol) == protocolOrTCP(want.Protocol) {
						return fmt.Errorf(
							"deploy: service %q publishes port %d, which %q still holds; set \"stopFirst\": true on %q so the old generation releases it first",
							b.Name, want.PublishedAs, s.Name, b.Name,
						)
					}
				}
			}
		}
	}
	return nil
}

func protocolOrTCP(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

// Compile-time check that the real docker client satisfies the widened
// ServiceDocker interface (ListServicesForProject is also used by cleanup).
var (
	_ ServiceDocker       = (*docker.Client)(nil)
	_ portPreflightDocker = (*docker.Client)(nil)
	_ stopFirstDocker     = (*docker.Client)(nil)
)
