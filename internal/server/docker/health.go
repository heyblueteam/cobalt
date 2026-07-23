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
		healths := c.containerHealthForService(ctx, name, filter)
		if len(healths) >= replicas {
			healthy, withHealth := 0, 0
			for _, health := range healths {
				if health != "" {
					withHealth++
				}
				if health == "healthy" {
					healthy++
				}
			}
			if withHealth == 0 || healthy >= replicas {
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
	if healths := c.containerHealthForService(ctx, serviceName); len(healths) > 0 {
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

// containerHealthForService returns the .State.Health.Status of every
// container backing the named swarm service. Empty string means the
// container declared no HEALTHCHECK; missing entries (e.g. when the
// inspect call fails) are simply skipped — the caller treats absence as
// "no healthcheck declared" and falls back to task-state readiness.
func (c *Client) containerHealthForService(ctx context.Context, serviceName string, extraLabelFilters ...string) []string {
	args := []string{"ps", "--filter", "label=com.docker.swarm.service.name=" + serviceName}
	for _, filter := range extraLabelFilters {
		args = append(args, "--filter", "label="+filter)
	}
	args = append(args, "--format", "{{.ID}}")
	out, err := c.output(ctx, args...)
	if err != nil {
		return nil
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	inspectArgs := append([]string{
		"inspect",
		"--format", `{{if .State.Health}}{{.State.Health.Status}}{{end}}`,
	}, ids...)
	out2, err := c.output(ctx, inspectArgs...)
	if err != nil {
		return nil
	}
	var healths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out2)), "\n") {
		healths = append(healths, strings.TrimSpace(line))
	}
	return healths
}
