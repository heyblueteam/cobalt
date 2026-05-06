//go:build integration

package deploy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/testutil/integration"
)

type cutoverHarness struct {
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

func newCutoverHarness(t *testing.T) *cutoverHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	h := &cutoverHarness{
		t:           t,
		ctx:         ctx,
		cancel:      cancel,
		networkName: "cobalt-cutover-" + randomID(),
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

func (h *cutoverHarness) startSwarm() {
	h.t.Helper()
	cmd := exec.CommandContext(h.ctx, "docker", "swarm", "init")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		h.t.Skipf("docker swarm init failed: %v", err)
	}
	h.swarmStop = func() {
		_ = exec.Command("docker", "swarm", "leave", "--force").Run()
	}
}

func (h *cutoverHarness) startCaddy() {
	h.t.Helper()
	h.caddyBaseURL, h.caddyStop = integration.StartCaddy(h.ctx, h.t, h.networkName)
	integration.WaitForCaddyReady(h.ctx, h.t, h.caddyBaseURL, 10*time.Second)
}

func (h *cutoverHarness) startDaemon() {
	h.t.Helper()
	h.daemonBaseURL, h.daemonStop = integration.StartDaemon(h.ctx, h.t, integration.DaemonOptions{
		NetworkName:   h.networkName,
		CaddyAdminURL: h.caddyBaseURL,
		DockerHost:    "unix:///var/run/docker.sock",
		Port:          8080,
	})
}

func (h *cutoverHarness) triggerReconcile() {
	h.t.Helper()
	integration.TriggerReconcile(h.ctx, h.t, h.daemonBaseURL)
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func TestCutover_HealthcheckFails_RollsBackCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Parallel()
	h := newCutoverHarness(t)

	reqBody := `{"name":"testproj","githubRepo":"test/repo","branch":"main"}`
	resp, err := http.Post(h.daemonBaseURL+"/api/projects", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("create project: status %d", resp.StatusCode)
	}

	deployResp, err := http.Post(h.daemonBaseURL+"/api/projects/testproj/deployments", "application/json", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deployResp.Body.Close()
	if deployResp.StatusCode/100 != 2 {
		t.Fatalf("enqueue: status %d", deployResp.StatusCode)
	}

	h.waitForDeploymentFailed(t, 60*time.Second)
}

func (h *cutoverHarness) waitForDeploymentFailed(t *testing.T, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(h.ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for deployment to fail")
		case <-ticker.C:
			resp, err := http.Get(h.daemonBaseURL + "/api/projects/testproj/deployments")
			if err != nil {
				continue
			}
			var result []map[string]any
			json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if len(result) > 0 && result[0]["status"] == string(cobaltapi.StateFailed) {
				return
			}
		}
	}
}