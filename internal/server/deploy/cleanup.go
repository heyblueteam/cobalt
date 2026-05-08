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

// cleanupOldServices stops every project service whose name doesn't match
// the current deployment number's prefix `{project}-{n}-`. Best-effort:
// per-service failures are logged and skipped. Never returns an error;
// the deploy is already marked successful and traffic is on the new
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
	var stale []string
	for _, s := range services {
		if !strings.HasPrefix(s.Name, currentPrefix) {
			stale = append(stale, s.Name)
		}
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
