package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WaitForServiceHealthy waits for `replicas` tasks of `name` to reach
// healthy status. It reads each task's `.Status.Health.Status` field; if
// the service has no healthcheck declared (no Health field on any task),
// it transparently falls back to WaitForServiceReady's task-state polling
// so services without healthchecks aren't forced to declare one.
//
// Same fail-fast as WaitForServiceReady: 3*replicas Shutdown/Failed/
// Rejected states aborts early rather than waiting out the timeout.
func (c *Client) WaitForServiceHealthy(ctx context.Context, name string, replicas int, timeout time.Duration) error {
	if replicas < 1 {
		replicas = 1
	}
	deadline := time.Now().Add(timeout)
	const pollInterval = 3 * time.Second

	for {
		statuses, err := c.taskStatuses(ctx, name)
		if err != nil {
			return fmt.Errorf("waitHealthy %s: %w", name, err)
		}

		var healthy, running, shutdown, withHealth int
		for _, s := range statuses {
			if s.Health != "" {
				withHealth++
				if s.Health == "healthy" {
					healthy++
				}
			}
			switch s.State {
			case "Running":
				running++
			case "Shutdown", "Failed", "Rejected":
				shutdown++
			}
		}

		// If no task has reported a Health field after our first poll, the
		// service has no healthcheck declared. Fall back to task-state
		// readiness so callers don't have to know the difference.
		if withHealth == 0 && len(statuses) > 0 {
			if running >= replicas {
				return nil
			}
		} else if healthy >= replicas {
			return nil
		}

		if shutdown >= 3*replicas {
			return fmt.Errorf("waitHealthy %s: %d shutdown/failed states (>= 3 * %d replicas)", name, shutdown, replicas)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitHealthy %s: timeout after %s (healthy=%d running=%d)", name, timeout, healthy, running)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// WaitForServiceDeploymentHealthy waits only for the running task containers
// created by deploymentNumber. Stable services retain the previous revision
// during a start-first update, so aggregate service health is insufficient.
func (c *Client) WaitForServiceDeploymentHealthy(ctx context.Context, name string, deploymentNumber, replicas int, timeout time.Duration) error {
	if replicas < 1 {
		replicas = 1
	}
	deadline := time.Now().Add(timeout)
	filter := LabelDeploymentNumber + "=" + strconv.Itoa(deploymentNumber)
	const pollInterval = 3 * time.Second
	for {
		containers, healths := c.containerHealthForService(ctx, name, filter)
		if containers >= replicas {
			// No healthcheck declared: containers being up is the whole signal,
			// matching WaitForServiceHealthy's task-state fallback.
			if len(healths) == 0 {
				return nil
			}
			healthy := 0
			for _, health := range healths {
				if health == "healthy" {
					healthy++
				}
			}
			if healthy >= replicas {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitHealthy %s deployment %d: timeout after %s", name, deploymentNumber, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// taskStatus is the slice of `docker service ps`'s task output we care
// about. .CurrentState is "Running 5 minutes ago"-style; .Status.Health
// is the health-probe verdict.
type taskStatus struct {
	State  string // "Running", "Shutdown", "Failed", "Rejected", ...
	Health string // "healthy", "unhealthy", "starting", or "" if no healthcheck
}

// taskStatuses reads docker service ps to enumerate each task's current
// state, then layers in HEALTHCHECK results from the live containers
// behind the service via `docker ps --filter` + `docker inspect`.
//
// Two-step because `docker service ps --format`'s context type doesn't
// expose .Status (so we can't pull health from the same call), and the
// per-task object's Status.ContainerStatus only carries an exit code, not
// the docker-engine-level health field. The container-side .State.Health
// is the authoritative source.
//
// State and Health are not paired by index — Caddy-swap only needs aggregate
// counts, and the order across `service ps` and `docker ps` isn't
// guaranteed to match anyway. Health is assigned to Running entries in
// arrival order so the WaitForServiceHealthy aggregates remain correct.
func (c *Client) taskStatuses(ctx context.Context, serviceName string) ([]taskStatus, error) {
	out, err := c.output(
		ctx,
		"service", "ps", serviceName,
		"--no-trunc",
		"--format", `{"current_state":"{{.CurrentState}}"}`,
	)
	if err != nil {
		return nil, err
	}
	var statuses []taskStatus
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			CurrentState string `json:"current_state"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		state := raw.CurrentState
		if i := strings.IndexByte(state, ' '); i > 0 {
			state = state[:i]
		}
		statuses = append(statuses, taskStatus{State: state})
	}

	// Layer in per-container health for Running tasks. Failures here are
	// non-fatal: WaitForServiceHealthy already falls back to task-state
	// readiness when no Health field is reported.
	if _, healths := c.containerHealthForService(ctx, serviceName); len(healths) > 0 {
		var idx int
		for i := range statuses {
			if statuses[i].State != "Running" {
				continue
			}
			if idx >= len(healths) {
				break
			}
			statuses[i].Health = healths[idx]
			idx++
		}
	}
	return statuses, nil
}

// containerHealthForService returns how many containers back the named swarm
// service, plus the .State.Health.Status of those whose image declares a
// HEALTHCHECK. An empty verdict slice means no healthcheck is declared, and
// callers fall back to task-state readiness.
//
// The count is deliberately taken from the container ids rather than from the
// verdict lines. `output` trims the whole blob, so a service whose containers
// all lack a healthcheck inspects to nothing but blank lines and arrives here
// as "" — indistinguishable from one container. Counting that string reported
// a single container however many were running, so no service with more than
// one replica and no HEALTHCHECK could ever satisfy a caller waiting on its
// replica count.
func (c *Client) containerHealthForService(ctx context.Context, serviceName string, extraLabelFilters ...string) (int, []string) {
	args := []string{"ps", "--filter", "label=com.docker.swarm.service.name=" + serviceName}
	for _, filter := range extraLabelFilters {
		args = append(args, "--filter", "label="+filter)
	}
	args = append(args, "--format", "{{.ID}}")
	out, err := c.output(ctx, args...)
	if err != nil {
		return 0, nil
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return 0, nil
	}
	inspectArgs := append([]string{
		"inspect",
		"--format", `{{if .State.Health}}{{.State.Health.Status}}{{end}}`,
	}, ids...)
	out2, err := c.output(ctx, inspectArgs...)
	if err != nil {
		return len(ids), nil
	}
	var healths []string
	for _, line := range strings.Split(string(out2), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			healths = append(healths, line)
		}
	}
	return len(ids), healths
}
