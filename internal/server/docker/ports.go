package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// publishFlagValue renders the long form of --publish/--publish-add/--publish-rm.
// The long form is required for removal: `--publish-rm 587` matches the
// target port, so it silently removes nothing when published != target.
func publishFlagValue(p PublishedPort) string {
	proto := p.Protocol
	if proto == "" {
		proto = "tcp"
	}
	v := fmt.Sprintf("published=%d,target=%d,protocol=%s", p.PublishedAs, p.FromContainerPort, proto)
	if p.Mode != "" {
		v += ",mode=" + p.Mode
	}
	return v
}

// ServicePublishedPorts returns the published ports in a service's actual spec.
// Swarm reserves ingress ports per service object, not per task: a service
// scaled to zero still blocks every other service from publishing them.
func (c *Client) ServicePublishedPorts(ctx context.Context, name string) ([]PublishedPort, error) {
	out, err := c.output(ctx, "service", "inspect", "--format", "{{json .Spec.EndpointSpec.Ports}}", name)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "null" {
		return nil, nil
	}
	var raw []struct {
		Protocol      string
		TargetPort    int
		PublishedPort int
		PublishMode   string
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("service %s: invalid published ports %q: %w", name, s, err)
	}
	ports := make([]PublishedPort, 0, len(raw))
	for _, r := range raw {
		if r.PublishedPort == 0 {
			continue // target-only entry: nothing reserved on the host
		}
		ports = append(ports, PublishedPort{
			PublishedAs:       r.PublishedPort,
			FromContainerPort: r.TargetPort,
			Protocol:          r.Protocol,
			Mode:              r.PublishMode,
		})
	}
	return ports, nil
}

// ReleaseServicePorts removes published ports from a stopped service so that a
// replacement service can publish them. The service object and the rest of its
// spec are kept for recovery.
func (c *Client) ReleaseServicePorts(ctx context.Context, name string, ports []PublishedPort) error {
	return c.updateServicePorts(ctx, name, "--publish-rm", ports)
}

// RestoreServicePorts re-adds ports released by ReleaseServicePorts.
func (c *Client) RestoreServicePorts(ctx context.Context, name string, ports []PublishedPort) error {
	return c.updateServicePorts(ctx, name, "--publish-add", ports)
}

func (c *Client) updateServicePorts(ctx context.Context, name, flag string, ports []PublishedPort) error {
	if len(ports) == 0 {
		return nil
	}
	args := []string{"service", "update", "--detach=true"}
	for _, p := range ports {
		args = append(args, flag, publishFlagValue(p))
	}
	return c.run(ctx, append(args, name)...)
}
