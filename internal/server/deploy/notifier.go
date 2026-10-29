package deploy

import (
	"context"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// DeploymentNotifier receives every terminal deployment. Implementations
// must be safe to call from a goroutine and must never block the deploy
// state machine.
type DeploymentNotifier interface {
	Notify(ctx context.Context, dep store.Deployment, status cobaltapi.State, runErr error) error
}
