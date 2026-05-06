//go:build integration

package deploy

import (
	"context"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/pkg/testutil/integration"
)

type chaosHarness struct {
	t             *testing.T
	ctx           context.Context
	cancel        context.CancelFunc
	networkName   string
	caddyBaseURL  string
	swarmStop     func()
	caddyStop     func()
	daemonBaseURL string
	daemonStop    func()
}

func newChaosHarness(t *testing.T) *chaosHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	h := &chaosHarness{
		t:           t,
		ctx:         ctx,
		cancel:      cancel,
		networkName: "cobalt-chaos-" + randomID(),
	}
	t.Cleanup(func() {
		cancel()
		if h.daemonStop != nil {
			h.daemonStop()
		}
		if h.caddyStop != nil {
			h.caddyStop()
		}
		if h.swarmStop != nil {
			h.swarmStop()
		}
	})
	h.startSwarm()
	h.startCaddy()
	h.startDaemon()
	return h
}

func (h *chaosHarness) startSwarm() {
	h.t.Helper()
	h.swarmStop = integration.StartSwarm(h.ctx, h.t)
}

func (h *chaosHarness) startCaddy() {
	h.t.Helper()
	h.caddyBaseURL, h.caddyStop = integration.StartCaddy(h.ctx, h.t, h.networkName)
	integration.WaitForCaddyReady(h.ctx, h.t, h.caddyBaseURL, 10*time.Second)
}

func (h *chaosHarness) startDaemon() {
	h.t.Helper()
	h.daemonBaseURL, h.daemonStop = integration.StartDaemon(h.ctx, h.t, integration.DaemonOptions{
		NetworkName:   h.networkName,
		CaddyAdminURL: h.caddyBaseURL,
		DockerHost:    "unix:///var/run/docker.sock",
		Port:          8080,
	})
}

func (h *chaosHarness) triggerReconcile() {
	h.t.Helper()
	integration.TriggerReconcile(h.ctx, h.t, h.daemonBaseURL)
}

func TestChaos_CaddyReturns500_RetriesWithBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Parallel()
	// TODO: implement once Caddy is injectable per-test
	t.Skip("requires Caddy that can be configured to fail on admin API")
}

func TestChaos_ConcurrentDeployAndReconcile(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Parallel()
	// TODO: implement once we have per-test project creation
	t.Skip("requires full daemon + project lifecycle")
}

func TestChaos_Reconciler_SkipsInFlightProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Parallel()
	// TODO: implement
	t.Skip("requires ability to observe reconciler skipping StateSwapping projects")
}