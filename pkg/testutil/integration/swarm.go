//go:build integration

package integration

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func StartSwarm(ctx context.Context, t testing.TB) (stop func()) {
	t.Helper()

	if err := exec.CommandContext(ctx, "docker", "swarm", "init").Run(); err != nil {
		t.Skipf("docker swarm init failed (docker daemon may not support swarm): %v", err)
	}
	stop = func() {
		_ = exec.Command("docker", "swarm", "leave", "--force").Run()
	}
	return stop
}

func dockerCmd(ctx context.Context, t testing.TB, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return string(bytes.TrimSpace(out))
}

func containerStop(ctx context.Context, t testing.TB, id string) {
	t.Helper()
	_ = exec.CommandContext(ctx, "docker", "kill", id).Run()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
}

func waitForHealthy(ctx context.Context, t testing.TB, serviceName string, replicas int, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			out := dockerCmd(ctx, t, "service", "ps", serviceName, "--format", "{{.CurrentState}}")
			t.Fatalf("service %s did not become healthy (replicas=%d, timeout). State:\n%s", serviceName, replicas, out)
		case <-ticker.C:
			out := dockerCmd(ctx, t, "service", "ps", serviceName, "--format", "{{.CurrentState}}")
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) >= replicas {
				allHealthy := true
				for _, line := range lines[:replicas] {
					if !strings.Contains(line, "Running") && !strings.Contains(line, "Ready") {
						allHealthy = false
						break
					}
				}
				if allHealthy {
					return
				}
			}
		}
	}
}