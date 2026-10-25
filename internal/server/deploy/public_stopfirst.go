package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

const stopFirstTimeout = 2 * time.Minute

type stopFirstDocker interface {
	ServiceDocker
	ServiceReplicas(context.Context, string) (int, error)
	SetServiceReplicas(context.Context, string, int) error
	WaitForServiceStopped(context.Context, string) error
}

type publicStopFirst struct {
	projectID        int64
	previous         *store.Deployment
	previousFile     *cobaltfile.Cobaltfile
	previousName     string
	previousReplicas int
	old              []string
	newName          string
}

func publicStopFirstEnabled(cf *cobaltfile.Cobaltfile) bool {
	web, ok := cf.Services["web"]
	return ok && web.Type == cobaltfile.TypeContainer && !web.ExposedInternally && web.StopFirst
}

// preparePublicStopFirst validates recovery metadata before touching a service.
// Keep the actual old service spec: its volume/env may differ from old source.
func preparePublicStopFirst(ctx context.Context, d stopFirstDocker, st SwapStore, project store.Project, dep store.Deployment) (*publicStopFirst, error) {
	s := &publicStopFirst{projectID: project.ID, newName: docker.ServiceName(project.Name, dep.Number, "web")}
	previous, err := st.GetLastSuccessfulDeployment(ctx, project.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if previous != nil {
		if previous.ResolvedCobaltfile == nil {
			return nil, fmt.Errorf("stopFirst: previous deployment has no recovery configuration")
		}
		cf, err := cobaltfile.Parse([]byte(*previous.ResolvedCobaltfile))
		if err != nil {
			return nil, fmt.Errorf("stopFirst: previous configuration: %w", err)
		}
		web, ok := cf.Services["web"]
		if !ok || web.Type != cobaltfile.TypeContainer || web.ExposedInternally {
			return nil, fmt.Errorf("stopFirst: previous deployment is not a public container web service")
		}
		s.previous, s.previousFile = previous, cf
		s.previousName = docker.ServiceName(project.Name, previous.Number, "web")
		if cf.UsesStablePublicWeb() {
			s.previousName = docker.StablePublicWebServiceName(project.ID)
		}
	}
	services, err := d.ListServicesForProject(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	found := previous == nil
	for _, svc := range services {
		gen, web := docker.WebGeneration(project.Name, svc.Name)
		if svc.Name != docker.StablePublicWebServiceName(project.ID) && (!web || gen == dep.Number) {
			continue
		}
		s.old = append(s.old, svc.Name)
		if svc.Name == s.previousName {
			found = true
			s.previousReplicas, err = d.ServiceReplicas(ctx, svc.Name)
			if err != nil {
				return nil, err
			}
			// Refuse to promise recovery of a service that is scaled down or already
			// has multiple writers. Operators must resolve that state explicitly.
			if s.previousReplicas != 1 {
				return nil, fmt.Errorf("stopFirst: previous public service must have exactly one replica (got %d)", s.previousReplicas)
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("stopFirst: previous service %q missing; refusing to stop without recovery", s.previousName)
	}
	return s, nil
}

func quiesceService(ctx context.Context, d stopFirstDocker, name string) error {
	if err := d.SetServiceReplicas(ctx, name, 0); err != nil {
		return err
	}
	return d.WaitForServiceStopped(ctx, name)
}

func (s *publicStopFirst) stopOld(ctx context.Context, d stopFirstDocker, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, stopFirstTimeout)
	defer cancel()
	fmt.Fprintln(out, "⏹ public stopFirst: waiting for old writers to exit (service downtime begins)")
	for _, name := range s.old {
		if err := quiesceService(ctx, d, name); err != nil {
			return fmt.Errorf("stopFirst: stop %s: %w", name, err)
		}
	}
	return nil
}

// recover first fences every failed/new writer, then resumes only the last
// successful service. Other grace generations must never become writers again.
// A fresh context permits recovery after deployment cancellation. Inspection
// failure leaves services stopped rather than risking two database writers.
func (s *publicStopFirst) recover(ctx context.Context, d stopFirstDocker, out io.Writer) error {
	services, err := d.ListServicesForProject(ctx, s.projectID)
	if err != nil {
		return err
	}
	for _, svc := range services {
		if svc.Name == s.newName {
			if err := quiesceService(ctx, d, svc.Name); err != nil {
				return fmt.Errorf("cannot prove replacement stopped: %w", err)
			}
			if err := d.RemoveService(ctx, svc.Name); err != nil {
				return err
			}
		}
	}
	// The original stop may have failed part-way through. Fence all old
	// services again before allowing even the previous service to restart.
	for _, name := range s.old {
		if err := quiesceService(ctx, d, name); err != nil {
			return fmt.Errorf("cannot prove old writer stopped: %w", err)
		}
	}
	if s.previous == nil {
		return nil
	}
	if err := d.SetServiceReplicas(ctx, s.previousName, s.previousReplicas); err != nil {
		return err
	}
	if err := d.WaitForServiceDeploymentHealthy(ctx, s.previousName, s.previous.Number, s.previousReplicas, HealthcheckTimeout); err != nil {
		return err
	}
	fmt.Fprintln(out, "↩ previous public service restarted; checking readiness and route")
	return nil
}
