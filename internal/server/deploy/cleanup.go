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

// cleanupOldServices removes superseded non-web services. Every public web
// endpoint, including a stable endpoint being reverted to numbered routing,
// remains until the reconciler confirms the new route and its grace floor.
// A second fast deploy must not bypass that floor for an older generation.
// Static/generator destinations have no container-route reaper, so their old
// services retain the existing one-generation fallback. Best-effort: the deployment is live.
func cleanupOldServices(
	ctx context.Context,
	log *slog.Logger,
	d CleanupDocker,
	project store.Project,
	dep store.Deployment,
	deferWebCleanup bool,
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
		if deferWebCleanup && s.Name == docker.StablePublicWebServiceName(project.ID) {
			continue // stable-mode revert is also retired by the guarded reaper
		}
		if strings.HasPrefix(s.Name, currentPrefix) {
			continue // current deployment — keep
		}
		if _, web := docker.WebGeneration(project.Name, s.Name); deferWebCleanup && web {
			continue // all web generations use the guarded reaper
		}
		if !deferWebCleanup && s.Name == grace {
			continue // preserve the existing non-public/static cleanup policy
		}
		stale = append(stale, s.Name)
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
