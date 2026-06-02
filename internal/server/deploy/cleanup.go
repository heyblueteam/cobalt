package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// CleanupDocker is the docker subset cleanupOldServices needs.
type CleanupDocker interface {
	ListServicesForProject(ctx context.Context, projectID int64) ([]docker.ServiceInfo, error)
	RemoveService(ctx context.Context, name string) error
}

// cleanupOldServices stops project services from previous deployments, with
// one deliberate exception: it keeps the most recent prior `*-web` generation
// alive as a grace fallback. If Caddy's compiled router lags the cutover by a
// deploy (it can, under reload pressure), that retained generation lets a
// stale request resolve to a working old build (200) instead of a
// since-deleted upstream (502) — the post-cutover incident this guards. The
// convergence reconciler reaps the retained generation once the new deployment
// is confirmed serving on the data plane (worker.reapSupersededWeb); failing
// that, the next deploy reaps it (it's no longer the *most recent* prior), so
// at most one extra web generation ever lingers.
//
// Best-effort: per-service failures are logged and skipped. Never returns an
// error; the deploy is already marked successful and traffic is on the new
// container.
func cleanupOldServices(
	ctx context.Context,
	log *slog.Logger,
	d CleanupDocker,
	project store.Project,
	dep store.Deployment,
	out io.Writer,
) {
	services, err := d.ListServicesForProject(ctx, project.ID)
	if err != nil {
		log.Warn("cleanup: list services", "project_id", project.ID, "error", err)
		return
	}
	currentPrefix := project.Name + "-" + itoaSimple(dep.Number) + "-"
	grace := graceWebService(services, project.Name, dep.Number)

	var stale []string
	for _, s := range services {
		if strings.HasPrefix(s.Name, currentPrefix) {
			continue // current deployment — keep
		}
		if s.Name == grace {
			continue // retained one cutover for grace
		}
		stale = append(stale, s.Name)
	}
	if grace != "" {
		fmt.Fprintf(out, "🛟 keeping %s one cutover for grace (reaped once #%d is confirmed live)\n",
			grace, dep.Number)
	}
	if len(stale) == 0 {
		return
	}
	fmt.Fprintf(out, "🧹 stopping %d service(s) from previous deployments\n", len(stale))
	for _, name := range stale {
		if err := d.RemoveService(ctx, name); err != nil {
			log.Warn("cleanup: remove service",
				"name", name, "project_id", project.ID, "error", err)
			fmt.Fprintf(out, "❌ stop %s: %s\n", name, err)
			continue
		}
		log.Info("cleanup: removed old service",
			"name", name, "project_id", project.ID, "current_deployment", dep.Number)
		fmt.Fprintf(out, "✅ stopped %s\n", name)
	}
}

// graceWebService returns the name of the most recent `*-web` service older
// than currentNumber, or "" if there is none. That single generation is kept
// alive briefly (see cleanupOldServices) so a router lagging the cutover serves
// the previous build instead of 502ing. Only web services qualify — worker and
// cron services have no public route, so there's nothing to fall back to.
func graceWebService(services []docker.ServiceInfo, projectName string, currentNumber int) string {
	best := -1
	var name string
	for _, s := range services {
		n, ok := docker.WebGeneration(projectName, s.Name)
		if !ok || n >= currentNumber {
			continue
		}
		if n > best {
			best = n
			name = s.Name
		}
	}
	return name
}

// itoaSimple is a tiny replacement for strconv.Itoa to avoid the import.
// (We keep this package's imports tight.)
func itoaSimple(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
