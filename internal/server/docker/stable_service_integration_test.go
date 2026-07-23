//go:build integration

package docker

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// TestReconcileStableService_RealSwarm verifies the operational contract that
// unit fakes cannot: one durable service name survives a start-first update.
// It owns a temporary single-node Swarm and is therefore opt-in via the
// integration build tag.
func TestReconcileStableService_RealSwarm(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := exec.CommandContext(ctx, "docker", "swarm", "init").Run(); err != nil {
		t.Skipf("docker swarm init failed: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "swarm", "leave", "--force").Run() })

	id := time.Now().UnixNano()
	name := StablePublicWebServiceName(id)
	defer func() { _ = exec.Command("docker", "service", "rm", name).Run() }()

	c := New()
	opts := ServiceCreateOpts{
		ProjectID: id, ProjectName: "stable-test", ServiceName: "web", DeploymentNumber: 1,
		Name: name, Image: "nginx:1.27-alpine",
	}
	if err := c.ReconcileStableService(ctx, opts); err != nil {
		t.Fatalf("create stable service: %v", err)
	}
	if err := c.WaitForServiceReady(ctx, name, 1, 2*time.Minute); err != nil {
		t.Fatalf("wait initial service: %v", err)
	}

	opts.DeploymentNumber = 2
	if err := c.ReconcileStableService(ctx, opts); err != nil {
		t.Fatalf("update stable service: %v", err)
	}
	if err := c.WaitForServiceReady(ctx, name, 1, 2*time.Minute); err != nil {
		t.Fatalf("wait updated service: %v", err)
	}

	out, err := exec.CommandContext(ctx, "docker", "service", "inspect", "--format", "{{.Spec.Name}}", name).Output()
	if err != nil {
		t.Fatalf("inspect stable service: %v", err)
	}
	if got := string(out); got != fmt.Sprintf("%s\n", name) {
		t.Errorf("service name after update = %q, want %q", got, name+"\n")
	}
}
