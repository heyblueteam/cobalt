// Package deploy owns the deployment state machine, queue, dispatcher,
// and the "blue line" through fetch → build → cutover. The Runner
// interface lets the dispatcher be tested without the actual build/cutover
// implementation, which lands in 8b/8c.
package deploy

import (
	"context"

	"github.com/heyblueteam/cobalt/internal/server/store"
)

// Runner executes a single deployment: clone, build, swap. The dispatcher
// owns lifecycle (state transitions, cancellation, terminal status); the
// Runner is responsible for the actual work.
//
// Implementations must respect ctx — cancellation comes through it. They
// must NOT mark terminal status themselves; returning nil signals success,
// returning an error signals failure (the dispatcher writes the row).
type Runner interface {
	Run(ctx context.Context, d store.Deployment) error
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc func(ctx context.Context, d store.Deployment) error

// Run satisfies Runner.
func (f RunnerFunc) Run(ctx context.Context, d store.Deployment) error { return f(ctx, d) }
