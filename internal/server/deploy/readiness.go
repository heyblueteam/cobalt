package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

const (
	// ReadinessTimeout is how long waitHTTPReady waits for a new web
	// service to start accepting TCP connections after Swarm reports
	// the task running. This runs *after* WaitForServiceHealthy, so
	// by the time we get here the container is already Running — 90s
	// is plenty of slack for app boot, and tight enough that a wedged
	// service surfaces failure long before the 5-minute swarm-side
	// timeout.
	//
	// (Disco uses 5 minutes for the equivalent probe. We're stricter
	// because we already burned the swarm-readiness budget in the
	// prior phase.)
	ReadinessTimeout = 90 * time.Second

	readinessPollInterval = 2 * time.Second

	// caddyServiceLabel selects the caddy task container in Swarm
	// installs. cobalt init deploys the stack as `cobalt`, so the
	// caddy service is `cobalt_caddy` and the actual container name
	// is `cobalt_caddy.<replica>.<task-id>` — too dynamic to
	// hardcode. We look it up by Swarm service label per probe.
	caddyServiceLabel = "com.docker.swarm.service.name=cobalt_caddy"

	// caddyContainerCompose is the bare container name used in the
	// non-Swarm `docker compose up` topology. Used as a fallback when
	// the Swarm-label lookup returns no match.
	caddyContainerCompose = "cobalt-caddy"
)

// ReadinessProber is the docker subset waitHTTPReady uses. *docker.Client
// satisfies it via Exec + FindContainerByLabel. Defined as an interface
// so unit tests can plug a fake without spinning up a container.
type ReadinessProber interface {
	Exec(ctx context.Context, container string, cmd []string, stdout, stderr io.Writer) error
	FindContainerByLabel(ctx context.Context, label string) (string, error)
}

// waitHTTPReady blocks until the cobaltfile's `web` service accepts a TCP
// connection on its configured port, or ReadinessTimeout elapses. Probes
// from inside cobalt-caddy via `nc -z` — same pattern disco uses, same
// busybox nc that ships in caddy:2-alpine.
//
// Skipped silently when:
//   - the cobaltfile has no `web` service (only-crons, only-worker projects)
//   - web.Type is not container (static + generator are file_server-served
//     by Caddy directly, no swarm service to probe)
//
// This runs after waitHealthyAll, so "Swarm says the container's running"
// is already true. If we time out here, the failure mode is "container
// running but not listening" — which is exactly the deploy-correctness
// gap that a swarm-task-state check leaves open.
func waitHTTPReady(
	ctx context.Context,
	p ReadinessProber,
	project store.Project,
	dep store.Deployment,
	cf *cobaltfile.Cobaltfile,
	out io.Writer,
) error {
	web, ok := cf.Services["web"]
	if !ok || web.Type != cobaltfile.TypeContainer {
		return nil
	}
	port := web.Port
	if port == 0 {
		port = cobaltfile.DefaultPort
	}
	serviceName := docker.ServiceName(project.Name, dep.Number, "web")
	if cf.UsesStablePublicWeb() {
		serviceName = docker.StablePublicWebServiceName(project.ID)
	}

	fmt.Fprintf(out, "🩺 probing %s:%d for readiness\n", serviceName, port)

	deadline := time.Now().Add(ReadinessTimeout)
	t0 := time.Now()
	var lastErr string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, msg := probeTCP(ctx, p, serviceName, port)
		if ready {
			fmt.Fprintf(out, "✅ web is listening on :%d (%s)\n",
				port, time.Since(t0).Round(time.Second))
			return nil
		}
		lastErr = msg
		if time.Now().After(deadline) {
			detail := lastErr
			if detail == "" {
				detail = "no response"
			}
			return fmt.Errorf(
				"deploy: web service %s did not accept connections on port %d within %s — last probe: %s",
				serviceName, port, ReadinessTimeout, detail,
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readinessPollInterval):
		}
	}
}

// probeTCP runs a single nc probe via the cobalt-caddy container. nc -z
// (zero-I/O scan) returns 0 iff the TCP connect succeeded; anything else
// means the service didn't accept the connection.
func probeTCP(ctx context.Context, p ReadinessProber, service string, port int) (bool, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	caddy := resolveCaddyContainer(probeCtx, p)
	cmd := []string{
		"nc", "-z", "-w", "3",
		service, fmt.Sprintf("%d", port),
	}
	var stderr bytes.Buffer
	err := p.Exec(probeCtx, caddy, cmd, io.Discard, &stderr)
	if err == nil {
		return true, ""
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	return false, snippetReadiness(msg)
}

// resolveCaddyContainer returns the name of the running caddy
// container — looked up by Swarm service label, with a fallback to
// the compose-mode literal name. Re-resolved every probe so a Swarm
// task replacement (which gives the new task a fresh container ID)
// doesn't wedge subsequent probes against a dead name.
func resolveCaddyContainer(ctx context.Context, p ReadinessProber) string {
	if name, err := p.FindContainerByLabel(ctx, caddyServiceLabel); err == nil && name != "" {
		return name
	}
	return caddyContainerCompose
}

func snippetReadiness(s string) string {
	const max = 160
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
