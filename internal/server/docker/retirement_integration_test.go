//go:build integration

package docker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRetireService_RealSwarm(t *testing.T) {
	if os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires disposable Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("swarm", "init")
	t.Cleanup(func() { _ = exec.Command("docker", "swarm", "leave", "--force").Run() })
	name := "retirement-test-web"
	t.Cleanup(func() { _ = exec.Command("docker", "service", "rm", name).Run() })
	run("service", "create", "--detach=true", "--name", name, "--stop-grace-period", "20s", "alpine:3.21", "sh", "-c", "trap 'sleep 4; exit 0' TERM; echo ready; while :; do sleep 1 & wait $!; done")
	deadline := time.Now().Add(time.Minute)
	for {
		logs := run("service", "logs", name)
		if strings.Contains(logs, "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service never ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	started := time.Now()
	if err := New().RetireService(ctx, name); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 4*time.Second {
		t.Fatalf("returned before graceful shutdown completed: %s", elapsed)
	}
	if got := run("ps", "-q", "--filter", "label=com.docker.swarm.service.name="+name); got != "" {
		t.Fatalf("tasks still running: %s", got)
	}
	t.Log("retirement waited for graceful shutdown; no running task remains")
}
