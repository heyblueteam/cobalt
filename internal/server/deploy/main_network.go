package deploy

import (
	"context"
	"fmt"
)

// MainNetworkName is the shared overlay network hooks attach to. Long-
// running services attach to this in addition to their per-deployment
// network so cross-service references via DNS aliases work.
const MainNetworkName = "cobalt-main"

// EnsureMainNetwork creates the cobalt-main overlay network if missing.
// Idempotent: an already-existing network is a no-op. Safe to call
// multiple times concurrently — docker network create returns an error
// for races, but NetworkExists check happens-before keeps races rare and
// the create-error path doesn't bubble.
func EnsureMainNetwork(ctx context.Context, d MainNetworkDocker) error {
	exists, err := d.NetworkExists(ctx, MainNetworkName)
	if err != nil {
		return fmt.Errorf("deploy: probe %s: %w", MainNetworkName, err)
	}
	if exists {
		return nil
	}
	return d.CreateMainNetwork(ctx, MainNetworkName)
}

// MainNetworkDocker is the docker subset EnsureMainNetwork needs. Defined
// as an interface so tests substitute fakes.
type MainNetworkDocker interface {
	NetworkExists(ctx context.Context, name string) (bool, error)
	CreateMainNetwork(ctx context.Context, name string) error
}
