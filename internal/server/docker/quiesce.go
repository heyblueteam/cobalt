package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ServiceReplicas reads the desired replica count before a stop-first cutover.
func (c *Client) ServiceReplicas(ctx context.Context, name string) (int, error) {
	out, err := c.output(ctx, "service", "inspect", "--format", "{{.Spec.Mode.Replicated.Replicas}}", name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("service %s: invalid replica count %q", name, out)
	}
	return n, nil
}

// SetServiceReplicas submits a scale request without waiting for convergence.
func (c *Client) SetServiceReplicas(ctx context.Context, name string, replicas int) error {
	return c.run(ctx, "service", "scale", "--detach=true", fmt.Sprintf("%s=%d", name, replicas))
}

// WaitForServiceStopped retains the service object until all tasks have exited.
// Desired replicas=0 and an accepted service-rm request do not prove exit.
// Cobalt runs on a single host: check local containers as well as Swarm tasks.
// Unknown/orphaned states and inspection errors fail closed.
func (c *Client) WaitForServiceStopped(ctx context.Context, name string) error {
	for {
		states, err := c.taskStates(ctx, name)
		if err != nil {
			return fmt.Errorf("wait stopped %s: %w", name, err)
		}
		stopped := true
		for _, state := range states {
			switch state {
			case "Shutdown", "Complete", "Failed", "Rejected", "Remove":
			default:
				stopped = false
			}
		}
		out, err := c.output(ctx, "ps", "--all", "--filter", "label=com.docker.swarm.service.name="+name, "--format", "{{.State}}")
		if err != nil {
			return fmt.Errorf("wait stopped %s containers: %w", name, err)
		}
		for _, state := range strings.Fields(string(out)) {
			if state != "exited" && state != "dead" {
				stopped = false
			}
		}
		if stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
