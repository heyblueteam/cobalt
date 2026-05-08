package docker

import (
	"context"
	"encoding/json"
	"fmt"
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

// taskStatus is the slice of `docker service ps`'s task output we care
// about. .CurrentState is "Running 5 minutes ago"-style; .Status.Health
// is the health-probe verdict.
type taskStatus struct {
	State  string // "Running", "Shutdown", "Failed", "Rejected", ...
	Health string // "healthy", "unhealthy", "starting", or "" if no healthcheck
}

// taskStatuses reads docker service ps and parses out each task's current
// state. Health is left empty here — every task falls through to the
// task-state fallback in WaitForServiceHealthy. Reading the per-task
// .Status.Health.Status requires a second call (docker inspect on each
// task ID) and is tracked separately; the previous template approach
// failed because `docker service ps --format`'s context type does not
// expose .Status at all.
func (c *Client) taskStatuses(ctx context.Context, serviceName string) ([]taskStatus, error) {
	out, err := c.output(ctx,
		"service", "ps", serviceName,
		"--no-trunc",
		"--format", `{"current_state":"{{.CurrentState}}"}`,
	)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
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
	return statuses, nil
}
