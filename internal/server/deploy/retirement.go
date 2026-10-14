package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/internal/server/webretirement"
)

type retirementStore interface {
	webretirement.Store
	GetLastSuccessfulDeployment(context.Context, int64) (*store.Deployment, error)
	ListPrimaryDomainsForProject(context.Context, int64) ([]string, error)
}

type retirementRoute interface {
	CurrentUpstream(context.Context, int64) (string, error)
}

type retirementProber interface {
	ServedDeployment(context.Context, string) (string, int, error)
}

// waitForWebCapacity runs under the cutover lock before hooks or any new
// containers start. It can reap while owning that lock, so admission cannot
// deadlock waiting for the background reconciler. It never switches routes.
func waitForWebCapacity(ctx context.Context, log *slog.Logger, st retirementStore, d webretirement.Docker, route retirementRoute, probe retirementProber, project store.Project, cf *cobaltfile.Cobaltfile, out io.Writer, poll time.Duration) error {
	if !deferPublicWebCleanup(cf) {
		return nil
	}
	services, err := d.ListServicesForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("web retirement: list services: %w", err)
	}
	if webretirement.HasCapacity(services, project, cf.MaxWebGenerations()) {
		return nil
	}

	live, err := st.GetLastSuccessfulDeployment(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("web retirement: cannot identify current deployment: %w", err)
	}
	if live.ResolvedCobaltfile == nil {
		return errors.New("web retirement: current deployment has no saved configuration")
	}
	liveCF, err := cobaltfile.Parse([]byte(*live.ResolvedCobaltfile))
	if err != nil {
		return err
	}

	// Bound an inconclusive probe or failed Docker cleanup. A canceled wait
	// leaves the current deployment and all still-draining services intact.
	grace := max(cf.WebGraceSeconds(), liveCF.WebGraceSeconds())
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(grace)*time.Second+2*time.Minute)
	defer cancel()
	fmt.Fprintf(out, "⏳ waiting for old web services to drain (maxGenerations=%d); no replacement started\n", cf.MaxWebGenerations())
	for {
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("web retirement: capacity wait ended; current deployment unchanged: %w", err)
		}
		confirmed, err := retirementRouteConfirmed(waitCtx, st, route, probe, project, *live, liveCF)
		if err != nil {
			return err
		}
		if confirmed {
			if err := webretirement.Reap(waitCtx, log, st, d, project, *live, liveCF); err != nil {
				return fmt.Errorf("web retirement: %w", err)
			}
		}
		services, err = d.ListServicesForProject(waitCtx, project.ID)
		if err != nil {
			return err
		}
		if webretirement.HasCapacity(services, project, cf.MaxWebGenerations()) {
			fmt.Fprintln(out, "✅ web generation capacity available")
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("web retirement: capacity wait ended; current deployment unchanged: %w", waitCtx.Err())
		case <-time.After(poll):
		}
	}
}

func retirementRouteConfirmed(ctx context.Context, st retirementStore, route retirementRoute, probe retirementProber, project store.Project, live store.Deployment, cf *cobaltfile.Cobaltfile) (bool, error) {
	if probe == nil || route == nil || !deferPublicWebCleanup(cf) {
		return false, nil
	}
	want := docker.ServiceName(project.Name, live.Number, "web")
	if cf.UsesStablePublicWeb() {
		want = docker.StablePublicWebServiceName(project.ID)
	}
	got, err := route.CurrentUpstream(ctx, project.ID)
	if err != nil {
		return false, err
	}
	if got != want {
		return false, nil
	}
	domains, err := st.ListPrimaryDomainsForProject(ctx, project.ID)
	if err != nil {
		return false, err
	}
	for _, domain := range domains {
		served, status, err := probe.ServedDeployment(ctx, domain)
		if err != nil || served == "" {
			continue
		}
		// Match the existing shared-route confirmation contract. A conclusive
		// divergent response blocks retirement; unavailable aliases may be skipped.
		return served == strconv.Itoa(live.Number) && status >= 200 && status < 500, nil
	}
	return false, nil
}
