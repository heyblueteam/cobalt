//go:build integration

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run against a disposable Docker daemon, never the host's existing Swarm.
// This exercises shared assets and a persistent HTTP connection through Caddy
// while Swarm replaces the backend behind the same service name/VIP.
func TestReconcileStableService_RealSwarm(t *testing.T) {
	if testing.Short() || os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires COBALT_DISPOSABLE_SWARM=1 and a disposable Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" || strings.HasPrefix(host, "unix:") {
		t.Fatal("explicit disposable DOCKER_HOST required; host socket is forbidden")
	}
	if os.Getenv("COBALT_DISPOSABLE_SWARM_OWNERSHIP") != "fix-stable-private-web" {
		t.Fatal("explicit disposable daemon ownership marker required")
	}
	if run("info", "--format", "{{.Swarm.LocalNodeState}}") != "active" {
		t.Fatal("caller must initialize owned disposable Swarm")
	}

	id := time.Now().UnixNano()
	name := StablePublicWebServiceName(id)
	network := fmt.Sprintf("stable-test-%d", id)
	volume := VolumeName(id, "assets")
	proxy := fmt.Sprintf("proxy-%d", id)
	client := fmt.Sprintf("client-%d", id)
	// The caller owns daemon lifecycle; this test never changes Swarm membership.
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", volume).Run() })
	t.Cleanup(func() { _ = exec.Command("docker", "service", "rm", name).Run() })
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", proxy, client).Run() })
	for _, image := range []string{"nginx:1.27-alpine", "caddy:2-alpine", "python:3.12-alpine"} {
		run("pull", image)
	}
	run("network", "create", "--driver", "overlay", "--attachable", network)
	run("volume", "create", volume)
	c := New()
	command := func(version int) string {
		archive := ""
		if version == 1 {
			archive = "printf old-asset > /usr/share/nginx/html/archive/old.js; "
		}
		return fmt.Sprintf("sh -c 'printf build-%d > /usr/share/nginx/html/index.html; %sexec nginx -g \"daemon off;\"'", version, archive)
	}
	opts := ServiceCreateOpts{
		ProjectID: id, ProjectName: "stable-test", ServiceName: "web", DeploymentNumber: 1,
		Name: name, Image: "nginx:1.27-alpine", Command: command(1),
		Networks:      []NetworkAttachment{{Name: network, Alias: name}},
		Volumes:       []ServiceVolume{{VolumeName: volume, DestinationPath: "/usr/share/nginx/html/archive"}},
		HealthCommand: "wget -q -O /dev/null http://127.0.0.1/", HealthInterval: time.Second, HealthStartPeriod: 10 * time.Second,
	}
	if err := c.ReconcileStableService(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForServiceDeploymentHealthy(ctx, name, 1, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Caddy owns the pooled backend connection; the client reuses one HTTP/1.1
	// connection to Caddy across the backend update, just like an edge proxy.
	config := fmt.Sprintf(":80 {\n reverse_proxy %s:80\n}\n", name)
	run("run", "-d", "--name", proxy, "--network", network, "caddy:2-alpine", "sh", "-c", "printf '%s' \"$1\" > /tmp/Caddyfile; exec caddy run --config /tmp/Caddyfile --adapter caddyfile", "sh", config)
	script := `import http.client,json,pathlib,time
c=http.client.HTTPConnection("` + proxy + `",80,timeout=5)
stats={"requests":0,"errors":[],"versions":[]}
end=time.monotonic()+120
while time.monotonic()<end:
 try:
  for path,expected in [("/",None),("/archive/old.js","old-asset")]:
   c.request("GET",path)
   r=c.getresponse(); body=r.read().decode()
   if r.status!=200 or (expected and body!=expected): raise Exception(str((path,r.status,body)))
   stats["requests"]+=1
   if path=="/" and body not in stats["versions"]: stats["versions"].append(body)
 except Exception as e:
  stats["errors"].append(str(e)); c.close()
 pathlib.Path("/tmp/result.json").write_text(json.dumps(stats))
 if "build-2" in stats["versions"] and stats["requests"]>=20: break
 time.sleep(.1)
print(json.dumps(stats),flush=True)
`
	// Wait for the proxy before starting the error-counted loop.
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, err := exec.CommandContext(ctx, "docker", "exec", proxy, "wget", "-qO-", "http://127.0.0.1/").CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "build-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy not ready: %v %s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	run("run", "-d", "--name", client, "--network", network, "python:3.12-alpine", "python", "-u", "-c", script)
	// Prove the persistent client has observed v1 before changing the backend.
	deadline = time.Now().Add(15 * time.Second)
	for {
		out, err := exec.CommandContext(ctx, "docker", "exec", client, "cat", "/tmp/result.json").CombinedOutput()
		if err == nil && strings.Contains(string(out), "build-1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client did not observe initial build: %v %s", err, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	opts.DeploymentNumber = 2
	opts.Command = command(2)
	if err := c.ReconcileStableService(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForServiceDeploymentHealthy(ctx, name, 2, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	run("wait", client)
	result := run("logs", client)
	if !strings.Contains(result, `"errors": []`) || !strings.Contains(result, `"build-1"`) || !strings.Contains(result, `"build-2"`) {
		t.Fatalf("pooled traffic or retained assets failed: %s", result)
	}
	t.Log(result)
	// Reconcile mount additions, replacements at the same target, and removal
	// using the real Docker update command. None may delete persistent data.
	extra := VolumeName(id, "extra")
	replacement := VolumeName(id, "replacement")
	run("volume", "create", extra)
	run("volume", "create", replacement)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", extra, replacement).Run() })
	for _, step := range []struct {
		number       int
		volumes      []ServiceVolume
		want, absent string
	}{
		{3, []ServiceVolume{{volume, "/usr/share/nginx/html/archive"}, {extra, "/extra"}}, extra, ""},
		{4, []ServiceVolume{{volume, "/usr/share/nginx/html/archive"}, {replacement, "/extra"}}, replacement, extra},
		{5, []ServiceVolume{{volume, "/usr/share/nginx/html/archive"}}, volume, replacement},
	} {
		opts.DeploymentNumber = step.number
		opts.Command = command(step.number)
		opts.Volumes = step.volumes
		if err := c.ReconcileStableService(ctx, opts); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitForServiceDeploymentHealthy(ctx, name, step.number, 1, time.Minute); err != nil {
			t.Fatal(err)
		}
		mounts := run("service", "inspect", "--format", "{{json .Spec.TaskTemplate.ContainerSpec.Mounts}}", name)
		if !strings.Contains(mounts, step.want) || (step.absent != "" && strings.Contains(mounts, step.absent)) {
			t.Fatalf("incorrect mount update at step %d: %s", step.number, mounts)
		}
	}
	run("volume", "inspect", volume, extra, replacement)

	if got := run("service", "inspect", "--format", "{{.Spec.Name}}", name); got != name {
		t.Fatalf("changed stable name: %s", got)
	}
	if got := run("service", "inspect", "--format", "{{json .Spec.TaskTemplate.ContainerSpec.Mounts}}", name); !strings.Contains(got, volume) {
		t.Fatalf("lost volume: %s", got)
	}
}
