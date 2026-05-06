package docker

import "context"

// CreateMainNetwork creates a long-lived overlay network with no
// deployment number — used by cobalt-main, the shared network hooks
// attach to. Distinct from CreateNetwork (per-deployment) because there's
// no project / deployment number to label it with.
func (c *Client) CreateMainNetwork(ctx context.Context, name string) error {
	args := []string{
		"network", "create",
		"--driver", "overlay",
		"--attachable",
		"--opt", "encrypted",
		"--label", "cobalt.network=main",
		name,
	}
	return c.run(ctx, args...)
}
