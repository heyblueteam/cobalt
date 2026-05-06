package docker

import (
	"context"
	"strings"
)

// CreateNetwork creates an overlay network for a project's deployment.
//
// We do NOT expose a RemoveNetwork: docker swarm has a long-standing bug
// (moby/moby#37338) that leaks IP addresses when overlay networks are
// removed. Upstream's discofile daemon documents the same — it never
// removes networks. Orphaned networks accumulate and need to be pruned out
// of band; image cleanup and routine `docker network prune` handle this.
func (c *Client) CreateNetwork(ctx context.Context, projectID int64, projectName string, deploymentNumber int) error {
	name := NetworkName(projectName, deploymentNumber)
	args := []string{
		"network", "create",
		"--driver", "overlay",
		"--attachable",
		"--opt", "encrypted",
	}
	for _, l := range serviceLabels(projectID, projectName, "", deploymentNumber) {
		args = append(args, "--label", l)
	}
	args = append(args, name)
	return c.run(ctx, args...)
}

// NetworkExists reports whether a network with the given name is present.
// Used to make CreateNetwork idempotent at the call site.
func (c *Client) NetworkExists(ctx context.Context, name string) (bool, error) {
	out, err := c.output(ctx,
		"network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}",
	)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// ConnectNetwork attaches an existing container to a network.
func (c *Client) ConnectNetwork(ctx context.Context, networkName, containerName string) error {
	return c.run(ctx, "network", "connect", networkName, containerName)
}

// DisconnectNetwork detaches a container from a network.
func (c *Client) DisconnectNetwork(ctx context.Context, networkName, containerName string) error {
	return c.run(ctx, "network", "disconnect", networkName, containerName)
}
