package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Deploy enqueues a deployment for the project and blocks until it
// reaches a terminal state. Fails the test if the deployment
// finishes in any state other than success.
//
// Default timeout is 10 minutes — fixture deploys finish in well
// under a minute, but a busy daemon plus image-pull cold cache can
// reach a few minutes legitimately.
func (p *Project) Deploy(t *testing.T) *cobaltapi.Deployment {
	t.Helper()
	return p.DeployWithTimeout(t, 10*time.Minute)
}

// DeployWithTimeout is Deploy with an explicit overall timeout.
func (p *Project) DeployWithTimeout(t *testing.T, timeout time.Duration) *cobaltapi.Deployment {
	t.Helper()
	final := p.deployRaw(t, timeout)
	if final.Status != cobaltapi.StateSuccess {
		t.Fatalf("deploy %d ended in %s (expected success)", final.ID, final.Status)
	}
	return final
}

// DeployExpectingFailure enqueues a deployment, waits for it to reach
// a terminal state, and fails the test if it succeeds. Used by
// scenarios that exercise deploy-failure paths (crash-loop, etc.) —
// the daemon should refuse to cut traffic over to a non-serving
// container.
func (p *Project) DeployExpectingFailure(t *testing.T, timeout time.Duration) *cobaltapi.Deployment {
	t.Helper()
	final := p.deployRaw(t, timeout)
	if final.Status == cobaltapi.StateSuccess {
		t.Fatalf("deploy %d succeeded; expected failure", final.ID)
	}
	return final
}

func (p *Project) deployRaw(t *testing.T, timeout time.Duration) *cobaltapi.Deployment {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := p.client.CreateDeployment(ctx, p.Name, cobaltapi.DeploymentCreateRequest{})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	final, err := p.WaitForDeploy(d.ID, timeout)
	if err != nil {
		t.Fatalf("wait for deploy %d: %v", d.ID, err)
	}
	return final
}

// WaitForDeploy polls /api/deployments/{id} until the deployment
// hits a terminal state or the timeout elapses. Returned err is
// non-nil only on transport problems or timeout — a "failed"
// terminal status is reported via the returned deployment.
func (p *Project) WaitForDeploy(id int64, timeout time.Duration) (*cobaltapi.Deployment, error) {
	deadline := time.Now().Add(timeout)
	pollEvery := 2 * time.Second
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		d, err := p.client.GetDeployment(ctx, id)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("get deployment: %w", err)
		}
		if isTerminal(d.Status) {
			return d, nil
		}
		time.Sleep(pollEvery)
	}
	return nil, fmt.Errorf("timeout waiting for deploy %d to reach terminal state", id)
}

func isTerminal(s cobaltapi.State) bool {
	switch s {
	case cobaltapi.StateSuccess, cobaltapi.StateFailed,
		cobaltapi.StateCanceled, cobaltapi.StateSkipped:
		return true
	}
	return false
}
