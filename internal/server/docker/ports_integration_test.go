//go:build integration

package docker

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Swarm reserves ingress ports per service object: scaling to zero does not
// free them. Releasing them must let a replacement publish the same port, and
// restoring must give the original spec back.
func TestReleaseServicePorts_RealSwarm(t *testing.T) {
	if os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires a disposable Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	run("pull", "busybox:1.37")

	c := New()
	ports := []PublishedPort{{PublishedAs: 47587, FromContainerPort: 587, Protocol: "tcp"}}
	create := func(gen int) error {
		name := ServiceName("ports-test", gen, "web")
		t.Cleanup(func() { _ = exec.Command("docker", "service", "rm", name).Run() })
		return c.CreateService(ctx, ServiceCreateOpts{
			ProjectID: 1, ProjectName: "ports-test", ServiceName: "web", DeploymentNumber: gen,
			Image: "busybox:1.37", Command: "sleep 3600", PublishedPorts: ports,
		})
	}
	old := ServiceName("ports-test", 1, "web")
	if err := create(1); err != nil {
		t.Fatal(err)
	}
	if err := c.SetServiceReplicas(ctx, old, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForServiceStopped(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := create(2); err == nil {
		t.Fatal("Swarm let a second service publish a port held by a zero-replica service; the release is no longer needed")
	}
	held, err := c.ServicePublishedPorts(ctx, old)
	if err != nil || len(held) != 1 || held[0].PublishedAs != 47587 {
		t.Fatalf("held %v err %v", held, err)
	}
	if err := c.ReleaseServicePorts(ctx, old, held); err != nil {
		t.Fatal(err)
	}
	if err := create(2); err != nil {
		t.Fatalf("replacement still blocked after release: %v", err)
	}
	run("service", "rm", ServiceName("ports-test", 2, "web"))
	if err := c.RestoreServicePorts(ctx, old, held); err != nil {
		t.Fatal(err)
	}
	if again, err := c.ServicePublishedPorts(ctx, old); err != nil || !reflect.DeepEqual(again, held) {
		t.Fatalf("restored %v err %v, want %v", again, err, held)
	}
}
