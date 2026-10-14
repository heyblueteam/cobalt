// Package webretirement shares retirement rules between the periodic reaper
// and deployment admission. Callers must own the project's cutover lock.
package webretirement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type Store interface {
	FirstSuccessfulDeploymentAfter(context.Context, int64, int, int) (*store.Deployment, error)
}

type Docker interface {
	ListServicesForProject(context.Context, int64) ([]docker.ServiceInfo, error)
	RetireService(context.Context, string) error
}

func IsWeb(project store.Project, name string) bool {
	_, numbered := docker.WebGeneration(project.Name, name)
	return numbered || name == docker.StablePublicWebServiceName(project.ID)
}

// Reap requires positive route confirmation by the caller. Each numbered
// service uses its own successor's cutover time, so later deploys do not reset
// its grace. Stable-to-numbered transitions conservatively use the latest cutover.
func Reap(ctx context.Context, log *slog.Logger, st Store, d Docker, project store.Project, live store.Deployment, cf *cobaltfile.Cobaltfile) error {
	if d == nil || live.FinishedAt == nil {
		return nil
	}
	services, err := d.ListServicesForProject(ctx, project.ID)
	if err != nil {
		return err
	}
	for _, service := range services {
		number, numbered := docker.WebGeneration(project.Name, service.Name)
		retiredStable := service.Name == docker.StablePublicWebServiceName(project.ID) && !cf.UsesStablePublicWeb()
		if !retiredStable && (!numbered || number >= live.Number) {
			continue
		}
		successor := &live
		if numbered {
			successor, err = st.FirstSuccessfulDeploymentAfter(ctx, project.ID, number, live.Number)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("retirement successor: %w", err)
			}
		}
		if successor.FinishedAt == nil {
			continue
		}
		// Honor the policy at the retiring cutover even if a subsequent deploy
		// shortens its own grace period.
		grace := cf.WebGraceSeconds()
		if successor.ResolvedCobaltfile != nil {
			policy, parseErr := cobaltfile.Parse([]byte(*successor.ResolvedCobaltfile))
			if parseErr != nil {
				return parseErr
			}
			grace = policy.WebGraceSeconds()
		}
		if time.Since(time.Unix(*successor.FinishedAt, 0)) < time.Duration(grace)*time.Second {
			continue
		}
		retireCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = d.RetireService(retireCtx, service.Name)
		cancel()
		if err != nil {
			return fmt.Errorf("retire %s: %w", service.Name, err)
		}
		log.Info("reaped superseded web service", "name", service.Name, "project_id", project.ID, "current", live.Number)
	}
	return nil
}

// HasCapacity counts service objects, including scaled-down services whose
// retirement did not finish. The replacement reserves one slot before startup.
// Stable updates reuse a service, but their start-first task overlap still
// reserves a generation's capacity.
func HasCapacity(services []docker.ServiceInfo, project store.Project, limit int) bool {
	count := 0
	for _, s := range services {
		if IsWeb(project, s.Name) {
			count++
		}
	}
	return count < limit
}
