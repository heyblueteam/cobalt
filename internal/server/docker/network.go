package docker

import (
	"context"
	"strconv"
	"strings"
)

// NetworkInfo identifies a single cobalt-managed overlay network parsed
// from `docker network ls`.
type NetworkInfo struct {
	Name             string // e.g. "cobalt-project-api-7"
	DeploymentNumber int    // 7
}

// CreateNetwork creates an overlay network for a project's deployment.
//
// Networks are named with the deployment number (`cobalt-project-{name}-{n}`)
// so we never re-create with the same name. This sidesteps the moby/moby#37338
// IP-leak bug that triggered upstream disco's blanket "never remove networks"
// rule. Stale networks from completed deployments are pruned hourly by
// worker.CleanupNetworks via RemoveNetwork.
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

// ListNetworksForProject returns every overlay network labeled for the
// given project, parsed into structured form. Networks whose names don't
// match the cobalt-project-{name}-{n} convention are skipped.
//
// Filtering is by label rather than name prefix so we never touch
// unlabeled networks (host bridges, ingress, disco leftovers from before
// cutover, hand-created networks).
func (c *Client) ListNetworksForProject(ctx context.Context, projectID int64, projectName string) ([]NetworkInfo, error) {
	out, err := c.output(ctx,
		"network", "ls",
		"--filter", FilterByProjectID(projectID),
		"--format", "{{.Name}}",
	)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	prefix := "cobalt-project-" + projectName + "-"
	var nets []NetworkInfo
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := name[len(prefix):]
		num, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		nets = append(nets, NetworkInfo{Name: name, DeploymentNumber: num})
	}
	return nets, nil
}

// RemoveNetwork deletes an overlay network by name. Treats "no such
// network" as success (idempotent). Docker refuses to remove a network
// with attached endpoints, which is our race-protection: if a service is
// still using it, the call fails and the caller should log-and-skip.
func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	if err := c.run(ctx, "network", "rm", name); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}
