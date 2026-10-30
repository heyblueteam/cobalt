//go:build integration

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStableHealthDefaultsMatchCLI_RealSwarm(t *testing.T) {
	if os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires caller-owned disposable Swarm")
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" || strings.HasPrefix(host, "unix:") || os.Getenv("COBALT_DISPOSABLE_SWARM_OWNERSHIP") != "fix-stable-private-web" {
		t.Fatal("explicit owned disposable DOCKER_HOST required")
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
	id := time.Now().UnixNano()
	name := StablePublicWebServiceName(id)
	image := fmt.Sprintf("cobalt-health-parity:%d", id)
	dockerfile := "FROM python:3.12-alpine\nHEALTHCHECK --interval=30s --timeout=5s --retries=4 CMD [\"python\",\"-c\",\"print('image check')\"]\nCMD [\"python\",\"-m\",\"http.server\",\"8080\"]\n"
	build := exec.CommandContext(ctx, "docker", "build", "-t", image, "-")
	build.Stdin = strings.NewReader(dockerfile)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build parity image: %v %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "service", "rm", name).Run()
		_ = exec.Command("docker", "image", "rm", image).Run()
	})
	cli := New()
	opts := ServiceCreateOpts{ProjectID: id, ProjectName: "health-parity", ServiceName: "web", DeploymentNumber: 1, Name: name, Image: image, Replicas: 1, HealthCommand: "python -c 'print(\"custom check\")'"}
	if err := cli.ReconcileStableService(ctx, opts); err != nil {
		t.Fatal(err)
	}
	health := func() map[string]any {
		raw := run("service", "inspect", name, "--format", "{{json .Spec.TaskTemplate.ContainerSpec.Healthcheck}}")
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if err := cli.WaitForServiceDeploymentHealthy(ctx, name, 1, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	effective := func(generation int) map[string]any {
		id := run("ps", "--filter", "label=com.docker.swarm.service.name="+name, "--filter", fmt.Sprintf("label=%s=%d", LabelDeploymentNumber, generation), "--format", "{{.ID}}")
		raw := run("inspect", strings.Fields(id)[0], "--format", "{{json .Config.Healthcheck}}")
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	created := effective(1)
	opts.DeploymentNumber = 2
	if err := cli.ReconcileStableService(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := cli.WaitForServiceDeploymentHealthy(ctx, name, 2, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	updated := effective(2)
	if !reflect.DeepEqual(created, updated) {
		a, _ := json.Marshal(created)
		b, _ := json.Marshal(updated)
		t.Fatalf("raw update differs from CLI create health: %s != %s", a, b)
	}
	if updated["Interval"] != float64(30*time.Second) || updated["Timeout"] != float64(5*time.Second) || updated["Retries"] != float64(4) {
		t.Fatalf("image health defaults missing: %v", updated)
	}
	opts.DeploymentNumber = 3
	opts.HealthCommand = ""
	if err := cli.ReconcileStableService(ctx, opts); err != nil {
		t.Fatal(err)
	}
	imageHealth := run("image", "inspect", image, "--format", "{{json .Config.Healthcheck}}")
	var expected map[string]any
	if err := json.Unmarshal([]byte(imageHealth), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(health(), expected) {
		t.Fatal("removing override failed to restore exact exec-form image health defaults")
	}
	report, _ := json.Marshal(updated)
	t.Logf("CLI_HEALTH_PARITY %s; override removal restores image exec-form", bytes.TrimSpace(report))
}
