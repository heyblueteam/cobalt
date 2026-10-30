//go:build integration

package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// stableIntegrationRunner uses the real CLI for every operation. Only the
// conventional readiness container name is mapped to this test's owned probe.
// This allows concurrent disposable-daemon tests without claiming cobalt-caddy.
type stableIntegrationRunner struct{ probe string }

func (r stableIntegrationRunner) Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	return r.RunWithEnv(ctx, nil, args, in, out, errout)
}

func (r stableIntegrationRunner) RunWithEnv(ctx context.Context, env map[string]string, args []string, in io.Reader, out, errout io.Writer) error {
	args = append([]string(nil), args...)
	if len(args) > 1 && args[0] == "exec" && args[1] == caddyContainerCompose {
		args[1] = r.probe
	}
	return (&docker.ExecRunner{}).RunWithEnv(ctx, env, args, in, out, errout)
}

// TestSharedStablePublicWeb_RealSwarm exercises the actual orchestration and
// cleanup paths with real Swarm service specs, tasks, DNS and HTTP. The admin
// API is deterministic; HTTP traffic additionally traverses a real Caddy proxy.
// The caller owns daemon creation/destruction. This test never init/leaves Swarm.
func TestSharedStablePublicWeb_RealSwarm(t *testing.T) {
	runSharedStablePublicWebSwarm(t, false)
}

func TestSharedStablePublicWebMigration_RealSwarm(t *testing.T) {
	runSharedStablePublicWebSwarm(t, true)
}

func TestExposedInternallyPreviousSemantics_RealSwarm(t *testing.T) {
	runSharedStablePublicWebSwarm(t, false, true)
}

func TestSharedStablePublicWebFailures_RealSwarm(t *testing.T) {
	runSharedStablePublicWebSwarm(t, false, false, true)
}

func runSharedStablePublicWebSwarm(t *testing.T, migration bool, internalOnly ...bool) {
	failures := len(internalOnly) > 1 && internalOnly[1]
	if testing.Short() || os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires explicitly owned disposable Swarm")
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" || host == "unix:///var/run/docker.sock" || host == "unix:///run/docker.sock" {
		t.Fatal("explicit non-host DOCKER_HOST is mandatory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+host)
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("docker %v: %v\n%s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	if state := run("info", "--format", "{{.Swarm.LocalNodeState}}"); state != "active" {
		t.Fatalf("caller must initialize isolated Swarm: %s", state)
	}
	// An independent ownership marker prevents the opt-in alone from authorizing
	// access to an existing host or remote production daemon.
	if marker := run("info", "--format", "{{.Name}}"); !strings.Contains(marker, "cobalt-private-web") && os.Getenv("COBALT_DISPOSABLE_SWARM_OWNERSHIP") != "fix-stable-private-web" {
		t.Fatal("set COBALT_DISPOSABLE_SWARM_OWNERSHIP=fix-stable-private-web after verifying disposable daemon ownership")
	}
	for _, image := range []string{"python:3.12-alpine", "python:3.13-alpine", "caddy:2-alpine"} {
		run("pull", image)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	name := "stableit" + suffix
	probe := "stable-probe-" + suffix
	proxy := "stable-proxy-" + suffix
	client := "stable-client-" + suffix
	db := openTestDB(t)
	pid := newProject(t, db, name)
	project, err := db.GetProjectByName(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AddDomain(ctx, pid, name+".example.test"); err != nil {
		t.Fatal(err)
	}
	cli := docker.NewWithRunner(stableIntegrationRunner{probe: probe})
	if err = EnsureMainNetwork(ctx, cli); err != nil {
		t.Fatal(err)
	}
	run("run", "-d", "--name", probe, "--network", MainNetworkName, "python:3.12-alpine", "sleep", "1800")
	t.Cleanup(func() {
		// Labels, not name prefixes, define exactly which services this test owns.
		cleanupCtx, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		services, _ := cli.ListServicesForProject(cleanupCtx, pid)
		for _, s := range services {
			_ = cli.RemoveService(cleanupCtx, s.Name)
		}
		for _, n := range []string{probe, proxy, client} {
			_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", n).Run()
		}
		out, _ := exec.CommandContext(cleanupCtx, "docker", "network", "ls", "--filter", fmt.Sprintf("label=cobalt.project.id=%d", pid), "--format", "{{.Name}}").Output()
		for _, n := range strings.Fields(string(out)) {
			_ = exec.CommandContext(cleanupCtx, "docker", "network", "rm", n).Run()
		}
		images, _ := exec.CommandContext(cleanupCtx, "docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}").Output()
		for _, image := range strings.Fields(string(images)) {
			if strings.HasPrefix(image, "cobalt/project-"+project.Name+"-") {
				_ = exec.CommandContext(cleanupCtx, "docker", "image", "rm", image).Run()
			}
		}
	})
	admin := newOrchCaddy(t)
	o := &Orchestrator{DB: db, Docker: cli, Caddy: caddy.NewHTTPClient(admin.URL, admin.Client()), DataDir: t.TempDir(), Log: quietLog(), LogWriter: io.Discard}
	o.Caddy.PatchVerifyBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond}
	config := func(version string) *cobaltfile.Cobaltfile {
		services := map[string]cobaltfile.Service{}
		image := "runtime1"
		if version != "one" {
			image = "runtime2"
		}
		for _, role := range []string{"web", "worker", "embed"} {
			services[role] = cobaltfile.Service{Type: cobaltfile.TypeContainer, Image: image, Port: 8080, Command: stableHTTPCommand(role, version), StopGracePeriodSeconds: 35, Health: &cobaltfile.Health{Command: `python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8080",timeout=2)'`}}
		}
		{
			for _, role := range []string{"worker", "embed"} {
				svc := services[role]
				svc.ExposedInternally = true
				services[role] = svc
			}
		}
		web := services["web"]
		web.PrivateAddress = true
		services["web"] = web
		maxGenerations := 100
		return &cobaltfile.Cobaltfile{Version: "1.0", StablePublicWeb: true, StablePublicWebNetwork: "shared", Services: services, Images: map[string]cobaltfile.Image{image: {Dockerfile: "Dockerfile", Context: "."}}, WebRetirement: &cobaltfile.WebRetirement{MaxGenerations: &maxGenerations}}
	}
	deploy := func(cf *cobaltfile.Cobaltfile, timeout time.Duration, rollback *store.Deployment) (store.Deployment, error) {
		t.Helper()
		dep := enqueueAndFetch(t, db, pid)
		dep.RollbackOf = nil
		if rollback != nil {
			dep.RollbackOf = &rollback.ID
		}
		raw, _ := json.Marshal(cf)
		if e := db.SetResolvedCobaltfile(ctx, dep.ID, string(raw)); e != nil {
			t.Fatal(e)
		}
		var built []BuiltService
		// Deterministic order proves a worker failure can follow an already-mutated
		// web service, rather than accidentally testing only the first failure.
		for _, role := range []string{"web", "worker", "embed"} {
			image := cf.Services[role].Image
			base := "python:3.12-alpine"
			if image == "runtime2" {
				base = "python:3.13-alpine"
			}
			tag := docker.InternalImageName(project.Name, image, dep.Number)
			run("tag", base, tag)
			built = append(built, BuiltService{Name: role, Service: cf.Services[role], ImageTag: tag})
		}
		phaseCtx, done := context.WithTimeout(ctx, timeout)
		defer done()
		var logs bytes.Buffer
		var e error
		if rollback != nil {
			e = o.rollbackRun(phaseCtx, o.Log, project, dep, map[string]string{"SETTING": fmt.Sprint(dep.Number)}, &logs)
		} else {
			e = o.Cutover(phaseCtx, project, dep, built, cf, map[string]string{"SETTING": fmt.Sprint(dep.Number)}, &logs)
		}
		state := cobaltapi.StateSuccess
		if e != nil {
			state = cobaltapi.StateFailed
		}
		_ = db.SetDeploymentStatus(ctx, dep.ID, state)
		t.Logf("cutover #%d: %v\n%s", dep.Number, e, logs.String())
		if e != nil {
			svcs, _ := cli.ListServicesForProject(ctx, pid)
			for _, service := range svcs {
				for _, args := range [][]string{{"service", "ps", service.Name, "--no-trunc"}, {"service", "logs", "--tail", "8", service.Name}} {
					b, _ := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
					t.Logf("failure diagnostic %s: %s", service.Name, b)
				}
			}
		}

		return dep, e
	}
	fetch := func(url string) string {
		t.Helper()
		return run("exec", probe, "python", "-c", "import urllib.request; print(urllib.request.urlopen("+fmt.Sprintf("%q", url)+",timeout=8).read().decode())")
	}
	await := func(url, want string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		var last string
		for time.Now().Before(deadline) {
			cmd := exec.CommandContext(ctx, "docker", "exec", probe, "python", "-c", "import urllib.request; print(urllib.request.urlopen("+fmt.Sprintf("%q", url)+",timeout=3).read().decode())")
			b, e := cmd.CombinedOutput()
			last = string(b)
			if e == nil && strings.TrimSpace(last) == want {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("HTTP %s want %q: %s", url, want, last)
	}
	if len(internalOnly) > 0 && internalOnly[0] {
		// This branch uses a real Caddy admin API and a real HTTP listener. The
		// legacy flag must publish only its shared alias, leaving the public route
		// untouched (404), just as it did before independent privateAddress existed.
		proxyConfig := "{\n admin 0.0.0.0:2019\n}\n:80 {\n respond no-route 404\n}\n"
		run("run", "-d", "--name", proxy, "--network", MainNetworkName, "--publish", "127.0.0.1::2019", "caddy:2-alpine", "sh", "-c", "printf '%s' \"$1\" > /tmp/Caddyfile; exec caddy run --config /tmp/Caddyfile --adapter caddyfile", "sh", proxyConfig)
		adminAddr := run("port", proxy, "2019/tcp")
		o.Caddy = caddy.NewHTTPClient("http://"+adminAddr, &http.Client{Timeout: 5 * time.Second})
		legacy := config("one")
		legacy.StablePublicWeb = false
		legacy.StablePublicWebNetwork = ""
		web := legacy.Services["web"]
		web.PrivateAddress = false
		web.ExposedInternally = true
		legacy.Services["web"] = web
		_, err := deploy(legacy, 4*time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		await("http://"+name+"-web:8080", "web:one")
		// A single expression avoids shell quoting and Python block syntax issues.
		script := "import http.client; c=http.client.HTTPConnection('" + proxy + "',80,timeout=5); c.request('GET','/'); print(c.getresponse().status)"
		if status := run("exec", probe, "python", "-c", script); status != "404" {
			t.Fatalf("legacy internal web acquired public route: %s", status)
		}
		t.Log("legacy exposedInternally: private HTTP200; real public Caddy404")
		previous, _ := cli.ListServicesForProject(ctx, pid)
		previousIDs := map[string]string{}
		for _, service := range previous {
			previousIDs[service.Name] = run("service", "inspect", service.Name, "--format", "{{.ID}}")
		}
		_, err = deploy(config("one"), 4*time.Minute, nil)
		if err == nil || !strings.Contains(err.Error(), "alias") {
			t.Fatalf("legacy alias collision was not explicitly rejected: %v", err)
		}
		await("http://"+name+"-web:8080", "web:one")
		after, _ := cli.ListServicesForProject(ctx, pid)
		if len(after) != len(previous) {
			t.Fatalf("alias preflight mutated legacy services: %+v", after)
		}
		for _, service := range after {
			if id := run("service", "inspect", service.Name, "--format", "{{.ID}}"); id != previousIDs[service.Name] {
				t.Fatalf("legacy service changed during collision preflight: %s", service.Name)
			}
		}
		t.Log("legacy private alias collision rejected before creating stable service")

		return
	}
	if !migration && !failures {
		badFirst := config("unready")
		web := badFirst.Services["web"]
		web.Command = "python -c 'raise SystemExit(24)'"
		badFirst.Services["web"] = web
		_, err := deploy(badFirst, 25*time.Second, nil)
		if err == nil {
			t.Fatal("first deployment with bad web succeeded")
		}
		live, err := cli.ListServicesForProject(ctx, pid)
		if err != nil || len(live) != 0 {
			t.Fatalf("failed first boot left services/aliases: %v %+v", err, live)
		}
		t.Log("failed first boot: all new stable services removed")
	}
	var numbered store.Deployment
	if migration {
		legacy := config("one")
		legacy.StablePublicWeb = false
		legacy.StablePublicWebNetwork = ""
		web := legacy.Services["web"]
		web.PrivateAddress = false
		legacy.Services["web"] = web
		var err error
		numbered, err = deploy(legacy, 4*time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		oldWeb := docker.ServiceName(name, numbered.Number, "web")
		await("http://"+oldWeb+":8080/peer/"+name+"-embed", "embed:one")
		// Cross-project fresh requests and a long request to the retiring internal
		// process remain live while the new alias owner warms and takes over.
		traffic := fmt.Sprintf(`import urllib.request,threading,time,pathlib,json
result={"errors":[],"requests":0,"long":None}
def slow():
 try: result['long']=urllib.request.urlopen('http://%s-embed:8080/slow',timeout=70).read().decode()
 except Exception as e: result['errors'].append('long: '+str(e))
th=threading.Thread(target=slow);th.start()
while not pathlib.Path('/tmp/stop').exists():
 for url in ['http://%s-embed:8080','http://%s:8080/peer/%s-embed']:
  try:
   body=urllib.request.urlopen(url,timeout=5).read().decode()
   if body!='embed:one': raise Exception(body)
   result['requests']+=1
  except Exception as e: result['errors'].append(str(e))
 time.sleep(.1)
th.join();print(json.dumps(result),flush=True)
`, name, name, oldWeb, name)
		run("run", "-d", "--name", client, "--network", MainNetworkName, "python:3.12-alpine", "python", "-u", "-c", traffic)
		time.Sleep(2 * time.Second)
	}
	first, e := deploy(config("one"), 4*time.Minute, nil)
	if e != nil {
		t.Fatal(e)
	}
	stableWeb := docker.StablePublicWebServiceName(pid)
	privateURL := "http://" + name + "-web:8080"
	await(privateURL, "web:one")
	services, e := cli.ListServicesForProject(ctx, pid)
	if e != nil {
		t.Fatal(e)
	}
	if (!migration && len(services) != 3) || (migration && len(services) < 3) {
		t.Fatalf("want stable web plus numbered siblings: %+v", services)
	}
	ids := map[string]string{}
	for _, s := range services {
		if s.Name != stableWeb {
			continue
		}
		ids[s.Name] = run("service", "inspect", s.Name, "--format", "{{.ID}}")
	}
	webVIP := run("service", "inspect", stableWeb, "--format", "{{json .Endpoint.VirtualIPs}}")
	var attachments []struct {
		Target  string
		Aliases []string
	}
	if err := json.Unmarshal([]byte(run("service", "inspect", stableWeb, "--format", "{{json .Spec.TaskTemplate.Networks}}")), &attachments); err != nil {
		t.Fatal(err)
	}
	mainID := run("network", "inspect", MainNetworkName, "--format", "{{.Id}}")
	if len(attachments) != 1 || attachments[0].Target != mainID {
		t.Fatalf("shared stable web must attach only cobalt-main: %+v", attachments)
	}
	for _, alias := range attachments[0].Aliases {
		if alias == "web" {
			t.Fatal("shared web unexpectedly provides a deployment short name")
		}
	}
	if got := fetch(privateURL + "/peer/" + name + "-embed"); got != "embed:one" {
		t.Fatalf("web shared dependency alias: %s", got)
	}

	// Numbered siblings retain their deployment-scoped short names.
	for _, pair := range [][2]string{{"worker", "embed"}, {"embed", "worker"}} {
		var source string
		for _, s := range services {
			if run("service", "inspect", s.Name, "--format", "{{index .Spec.Labels \"cobalt.service.name\"}}") == pair[0] {
				source = s.Name
			}
		}
		if source == "" {
			t.Fatalf("source service %s missing", pair[0])
		}
		container := run("ps", "--filter", "label=com.docker.swarm.service.name="+source, "--format", "{{.ID}}")
		got := run("exec", container, "python", "-c", "import urllib.request; print(urllib.request.urlopen('http://"+pair[1]+":8080',timeout=5).read().decode())")
		if got != pair[1]+":one" {
			t.Fatalf("short name %v: %s", pair, got)
		}
		got = run("exec", container, "python", "-c", "import urllib.request; print(urllib.request.urlopen('http://"+name+"-web:8080',timeout=5).read().decode())")
		if got != "web:one" {
			t.Fatalf("numbered sibling->private stable web: %s", got)
		}
	}
	if migration {
		oldWeb := docker.ServiceName(name, numbered.Number, "web")
		await("http://"+oldWeb+":8080/peer/"+name+"-embed", "embed:one")
		run("exec", client, "touch", "/tmp/stop")
		run("wait", client)
		traffic := run("logs", client)
		var result struct {
			Errors   []string `json:"errors"`
			Requests int      `json:"requests"`
			Long     string   `json:"long"`
		}
		if err := json.Unmarshal([]byte(traffic), &result); err != nil || len(result.Errors) != 0 || result.Requests < 10 || result.Long != "embed:one" {
			t.Fatalf("numbered->stable internal/old-web continuity: %s (%v)", traffic, err)
		}
		t.Logf("numbered->stable internal/old-web traffic: %s", traffic)
		legacy := config("one")
		legacy.StablePublicWeb = false
		legacy.StablePublicWebNetwork = ""
		web := legacy.Services["web"]
		web.PrivateAddress = false
		legacy.Services["web"] = web
		reverted, err := deploy(legacy, 4*time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		await("http://"+docker.ServiceName(name, reverted.Number, "web")+":8080/peer/"+name+"-embed", "embed:one")
		privateLookup := exec.CommandContext(ctx, "docker", "exec", probe, "python", "-c", "import socket;socket.getaddrinfo('"+name+"-web',8080)")
		if output, lookupErr := privateLookup.CombinedOutput(); lookupErr == nil {
			t.Fatalf("stable->numbered left obsolete private alias: %s", output)
		}
		await("http://"+stableWeb+":8080/peer/"+name+"-embed", "embed:one")
		await("http://"+name+"-embed:8080", "embed:one")
		// A second migration must reconcile existing retained stable services too.
		_, err = deploy(config("one"), 4*time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		await(privateURL, "web:one")
		await("http://"+stableWeb+":8080/peer/"+name+"-embed", "embed:one")
		return
	}
	// Real Caddy provides an independent public proxy path; the private path
	// resolves the same service from a different container on cobalt-main.
	proxyConfig := fmt.Sprintf(":80 {\n reverse_proxy %s:8080\n}\n", stableWeb)
	run("run", "-d", "--name", proxy, "--network", MainNetworkName, "caddy:2-alpine", "sh", "-c", "printf '%s' \"$1\" > /tmp/Caddyfile; exec caddy run --config /tmp/Caddyfile --adapter caddyfile", "sh", proxyConfig)
	await("http://"+proxy, "web:one")
	if failures {
		snapshot, err := cli.SnapshotStableService(ctx, stableWeb)
		if err != nil {
			t.Fatal(err)
		}
		siblingIDs := map[string]string{}
		for _, service := range services {
			if service.Name != stableWeb {
				siblingIDs[service.Name] = run("service", "inspect", service.Name, "--format", "{{.ID}}")
			}
		}
		assertRestored := func(label string) {
			t.Helper()
			await(privateURL, "web:one")
			await("http://"+proxy, "web:one")
			restored, err := cli.SnapshotStableService(ctx, stableWeb)
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			if json.Unmarshal(snapshot, &a) != nil || json.Unmarshal(restored, &b) != nil {
				t.Fatal("invalid snapshot JSON")
			}
			want, _ := json.Marshal(a)
			got, _ := json.Marshal(b)
			if !bytes.Equal(want, got) {
				t.Fatalf("%s did not restore exact stable web spec\nwant %s\ngot %s", label, want, got)
			}
			live, err := cli.ListServicesForProject(ctx, pid)
			if err != nil || len(live) != 3 {
				t.Fatalf("%s failed dependency cleanup: %v %+v", label, err, live)
			}
			for _, service := range live {
				if service.Name != stableWeb && run("service", "inspect", service.Name, "--format", "{{.ID}}") != siblingIDs[service.Name] {
					t.Fatalf("%s removed previous sibling %s", label, service.Name)
				}
			}
			t.Logf("%s: exact previous web spec, public/private HTTP and previous siblings restored", label)
		}
		internal := config("internal-transition")
		internal.StablePublicWeb = false
		internal.StablePublicWebNetwork = ""
		internalWeb := internal.Services["web"]
		internalWeb.PrivateAddress = false
		internalWeb.ExposedInternally = true
		internal.Services["web"] = internalWeb
		if _, err := deploy(internal, 4*time.Minute, nil); err == nil || !strings.Contains(err.Error(), "first deploy numbered public web") {
			t.Fatalf("unsafe direct internal transition not rejected: %v", err)
		}
		assertRestored("direct stable public to internal preflight rejection")
		bad := config("boot-failure")
		web := bad.Services["web"]
		web.Command = "python -c 'raise SystemExit(31)'"
		bad.Services["web"] = web
		if _, err := deploy(bad, 25*time.Second, nil); err == nil {
			t.Fatal("bad web boot unexpectedly succeeded")
		}
		assertRestored("web boot failure")
		if _, err := deploy(config("canceled"), 8*time.Second, nil); err == nil {
			t.Fatal("canceled cutover unexpectedly succeeded")
		}
		assertRestored("cancellation")
		// Reject only the incoming port; the previous route remains recoverable.
		rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if r.Method == http.MethodPatch && strings.Contains(string(body), stableWeb+":8081") {
				http.Error(w, "injected new-port swap failure", 503)
				return
			}
			admin.handle(w, r)
		}))
		defer rejecting.Close()
		o.Caddy = caddy.NewHTTPClient(rejecting.URL, rejecting.Client())
		o.Caddy.PatchVerifyBackoff = []time.Duration{time.Millisecond}
		changedPort := config("port-failure")
		web = changedPort.Services["web"]
		web.Port = 8081
		web.Command = stableHTTPCommand("web", "port-failure", 8081)
		web.Health = &cobaltfile.Health{Command: `python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8081",timeout=2)'`}
		changedPort.Services["web"] = web
		if _, err := deploy(changedPort, 4*time.Minute, nil); err == nil {
			t.Fatal("new-port Caddy rejection unexpectedly succeeded")
		}
		assertRestored("Caddy swap failure with changed port")
		admin.mu.Lock()
		dial := admin.upstreams[pid]
		admin.mu.Unlock()
		if dial != stableWeb+":8080" {
			t.Fatalf("previous Caddy route port not restored: %s", dial)
		}
		o.Caddy = caddy.NewHTTPClient(admin.URL, admin.Client())
		o.DataPlaneProber = stableHTTPDataPlane{cli: cli, probe: probe, proxy: proxy, served: fmt.Sprint(first.Number)}
		if _, err := deploy(config("dataplane-failure"), 4*time.Minute, nil); err == nil {
			t.Fatal("data-plane mismatch unexpectedly succeeded")
		}
		assertRestored("data-plane mismatch")
		o.DataPlaneProber = nil
		failedWorker := config("recovery-injection")
		worker := failedWorker.Services["worker"]
		worker.Command = "python -c 'raise SystemExit(41)'"
		failedWorker.Services["worker"] = worker
		o.Docker = docker.NewWithRunner(stableFailureRunner{base: stableIntegrationRunner{probe: probe}, blockRestoreGeneration: fmt.Sprint(first.Number)})
		if _, err := deploy(failedWorker, 25*time.Second, nil); err == nil || !strings.Contains(err.Error(), "stable web recovery failed") {
			t.Fatalf("restore failure not surfaced: %v", err)
		}
		retained, err := cli.ListServicesForProject(ctx, pid)
		if err != nil || len(retained) != 5 {
			t.Fatalf("restore failure discarded dependencies: %v %+v", err, retained)
		}
		if err := cli.RestoreStableService(ctx, stableWeb, snapshot); err != nil {
			t.Fatal(err)
		}
		if err := cli.WaitForStableServiceSettled(ctx, stableWeb); err != nil {
			t.Fatal(err)
		}
		for _, service := range retained {
			if service.Name != stableWeb && siblingIDs[service.Name] == "" {
				if err := cli.RemoveService(ctx, service.Name); err != nil {
					t.Fatal(err)
				}
			}
		}
		o.Docker = cli
		assertRestored("manual recovery after injected restore error")
		o.Docker = docker.NewWithRunner(stableFailureRunner{base: stableIntegrationRunner{probe: probe}, preserveNames: siblingIDs, blockCleanup: true})
		if _, err := deploy(failedWorker, 25*time.Second, nil); err == nil || !strings.Contains(err.Error(), "injected failed dependency cleanup") {
			t.Fatalf("cleanup failure not surfaced: %v", err)
		}
		retained, err = cli.ListServicesForProject(ctx, pid)
		if err != nil || len(retained) != 5 {
			t.Fatalf("cleanup failure not observable: %v %+v", err, retained)
		}
		await(privateURL, "web:one")
		await("http://"+proxy, "web:one")
		for _, service := range retained {
			if service.Name != stableWeb && siblingIDs[service.Name] == "" {
				if err := cli.RemoveService(ctx, service.Name); err != nil {
					t.Fatal(err)
				}
			}
		}
		o.Docker = cli
		assertRestored("cleanup error retains observable failed dependencies")
		return
	}
	// Open a long public request before updating and require its old generation
	// body to complete, while fresh public/private requests converge to v2.
	longScript := fmt.Sprintf(`import urllib.request,threading,time,json
result={"long":None,"errors":[],"requests":0,"new":False}
def slow():
 try: result['long']=urllib.request.urlopen('http://%s/slow',timeout=60).read().decode()
 except Exception as e: result['errors'].append('slow: '+str(e))
thread=threading.Thread(target=slow);thread.start()
end=time.monotonic()+100
while time.monotonic()<end:
 try:
  bodies=[urllib.request.urlopen(url,timeout=5).read().decode() for url in ['http://%s','%s']]
  result['requests']+=2
  if bodies==['web:two','web:two']: result['new']=True
 except Exception as e: result['errors'].append(str(e))
 if result['new'] and not thread.is_alive(): break
 time.sleep(.1)
thread.join();print(json.dumps(result),flush=True)
`, proxy, proxy, privateURL)
	run("run", "-d", "--name", client, "--network", MainNetworkName, "python:3.12-alpine", "python", "-u", "-c", longScript)
	time.Sleep(2 * time.Second)
	second, e := deploy(config("two"), 4*time.Minute, nil)
	if e != nil {
		t.Fatal(e)
	}
	await(privateURL, "web:two")
	await("http://"+proxy, "web:two")
	if got := fetch(privateURL + "/peer/" + name + "-worker"); got != "worker:two" {
		t.Fatalf("updated worker settings: %s", got)
	}
	if got := fetch(privateURL + "/peer/" + name + "-embed"); got != "embed:two" {
		t.Fatalf("updated embed settings: %s", got)
	}
	if got := fetch(privateURL + "/setting"); got != fmt.Sprint(second.Number) {
		t.Fatalf("updated environment: %s", got)
	}
	run("wait", client)
	traffic := run("logs", client)
	var trafficResult struct {
		Long     string   `json:"long"`
		Errors   []string `json:"errors"`
		Requests int      `json:"requests"`
		New      bool     `json:"new"`
	}
	if err := json.Unmarshal([]byte(traffic), &trafficResult); err != nil {
		t.Fatalf("traffic result: %v: %s", err, traffic)
	}
	if trafficResult.Long != "web:one" || len(trafficResult.Errors) != 0 || trafficResult.Requests < 10 || !trafficResult.New {
		t.Fatalf("public/private traffic across update: %s", traffic)
	}
	t.Logf("public/private traffic and long request: %s", traffic)
	for n, id := range ids {
		if got := run("service", "inspect", n, "--format", "{{.ID}}"); got != id {
			t.Fatalf("stable service %s recreated: %s -> %s", n, id, got)
		}
	}
	if got := run("service", "inspect", stableWeb, "--format", "{{json .Endpoint.VirtualIPs}}"); got != webVIP {
		t.Fatalf("stable VIP changed: %s -> %s", webVIP, got)
	}
	services, e = cli.ListServicesForProject(ctx, pid)
	if e != nil || len(services) != 3 {
		t.Fatalf("cleanup lost current siblings: %v %+v", e, services)
	}
	for _, service := range services {
		if service.Name != stableWeb {
			continue
		}
		snapshot, err := cli.SnapshotStableService(ctx, service.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile("/tmp/stable-it-snapshot-"+service.Name+".json", snapshot, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A failed worker boot must restore the previous stable web snapshot, including
	// a web service that was already updated successfully earlier in the phase.
	bad := config("bad")
	worker := bad.Services["worker"]
	worker.Command = "python -c 'raise SystemExit(23)'"
	bad.Services["worker"] = worker
	_, e = deploy(bad, 25*time.Second, nil)
	if e == nil {
		t.Fatal("failed worker boot unexpectedly succeeded")
	}
	for _, service := range services {
		if service.Name != stableWeb {
			continue
		}
		raw := run("service", "inspect", service.Name, "--format", "{{json .}}")
		_ = os.WriteFile("/tmp/stable-it-after-"+service.Name+".json", []byte(raw), 0o600)
	}
	await(privateURL, "web:two")
	for _, s := range services {
		if s.Name != stableWeb {
			continue
		}
		n := run("service", "inspect", s.Name, "--format", "{{index .Spec.Labels \"cobalt.deployment.number\"}}")
		if n != fmt.Sprint(second.Number) {
			t.Fatalf("failed deployment left changed snapshot %s label=%s", s.Name, n)
		}
	}
	// Rollback reconstructs the historical resolved config and cached image via
	// rollbackRun, then exercises the same real cutover and recovery lifecycle.
	rolled, e := deploy(config("one"), 4*time.Minute, &first)
	if e != nil {
		t.Fatal(e)
	}
	await(privateURL, "web:one")
	await("http://"+proxy, "web:one")
	await("http://"+stableWeb+":8080/peer/"+name+"-worker", "worker:one")
	live, err := cli.ListServicesForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range live {
		if service.Name != stableWeb {
			_, worker := docker.Generation(name, "worker", service.Name)
			_, embed := docker.Generation(name, "embed", service.Name)
			if service.Name != docker.ServiceName(name, rolled.Number, "worker") && service.Name != docker.ServiceName(name, rolled.Number, "embed") || !worker && !embed {
				t.Fatalf("rollback kept superseded numbered service: %+v", service)
			}
		}
	}

	off := config("one")
	web := off.Services["web"]
	web.PrivateAddress = false
	off.Services["web"] = web
	_, e = deploy(off, 4*time.Minute, nil)
	if e != nil {
		t.Fatal(e)
	}
	await("http://"+proxy, "web:one")
	cmd := exec.CommandContext(ctx, "docker", "exec", probe, "python", "-c", "import socket; socket.getaddrinfo('"+name+"-web',8080)")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("private alias still resolves after opt-out: %s", output)
	}
	_, e = deploy(config("one"), 4*time.Minute, nil)
	if e != nil {
		t.Fatal(e)
	}
	await(privateURL, "web:one")
	await("http://"+proxy, "web:one")
	t.Log("existing stable service private alias opt-out/on preserved public HTTP")
}

func stableHTTPCommand(role, version string, ports ...int) string {
	port := 8080
	if len(ports) > 0 {
		port = ports[0]
	}
	source := fmt.Sprintf(`import http.server,os,signal,threading,time,urllib.request
signal.signal(signal.SIGTERM,lambda *a:threading.Thread(target=lambda:(time.sleep(35),os._exit(0)),daemon=True).start())
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  body=%q
  if self.path=='/slow': time.sleep(20)
  if self.path=='/setting': body=os.environ.get('SETTING','missing')
  if self.path.startswith('/peer/'): body=urllib.request.urlopen('http://'+self.path.split('/')[-1]+':8080',timeout=5).read().decode()
  self.send_response(200); self.end_headers(); self.wfile.write(body.encode())
 def log_message(self,*a): pass
http.server.ThreadingHTTPServer(('0.0.0.0',8080),H).serve_forever()
`, role+":"+version)
	source = strings.ReplaceAll(source, "0.0.0.0',8080", "0.0.0.0',"+fmt.Sprint(port))
	return "python -u -c 'import base64;exec(base64.b64decode(\"" + base64.StdEncoding.EncodeToString([]byte(source)) + "\"))'"
}

// stableHTTPDataPlane performs a real request through Caddy, then injects only
// the deployment-identity mismatch. Recovery still must serve real HTTP200.
type stableHTTPDataPlane struct {
	cli                  *docker.Client
	probe, proxy, served string
}

func (p stableHTTPDataPlane) FindContainerByLabel(context.Context, string) (string, error) {
	return p.probe, nil
}

func (p stableHTTPDataPlane) Exec(ctx context.Context, container string, cmd []string, out, errout io.Writer) error {
	if len(cmd) > 1 && cmd[0] == "sh" && strings.Contains(strings.Join(cmd, " "), "GET / HTTP") {
		script := "import http.client; c=http.client.HTTPConnection('" + p.proxy + "',80,timeout=5); c.request('GET','/'); r=c.getresponse(); r.read(); print('HTTP/1.1 '+str(r.status)+' OK\r\nX-Cobalt-Deployment: " + p.served + "\r\n\r\n',end='')"
		return p.cli.Exec(ctx, p.probe, []string{"python", "-c", script}, out, errout)
	}
	return p.cli.Exec(ctx, p.probe, cmd, out, errout)
}

// stableFailureRunner injects transport/cleanup failures while every other
// Docker operation still executes against the caller-owned real Swarm.
type stableFailureRunner struct {
	base                   stableIntegrationRunner
	blockRestoreGeneration string
	preserveNames          map[string]string
	blockCleanup           bool
}

func (r stableFailureRunner) Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	return r.RunWithEnv(ctx, nil, args, in, out, errout)
}
func (r stableFailureRunner) RunWithEnv(ctx context.Context, env map[string]string, args []string, in io.Reader, out, errout io.Writer) error {
	if r.blockCleanup && len(args) >= 3 && args[0] == "service" && args[1] == "rm" && r.preserveNames[args[2]] == "" {
		return errors.New("injected failed dependency cleanup")
	}
	if r.blockRestoreGeneration != "" && len(args) == 2 && args[0] == "system" && args[1] == "dial-stdio" {
		raw, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		in = bytes.NewReader(raw)
		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
		if err != nil {
			return err
		}
		var spec struct{ Labels map[string]string }
		err = json.NewDecoder(req.Body).Decode(&spec)
		_ = req.Body.Close()
		if err != nil {
			return err
		}
		if spec.Labels[docker.LabelDeploymentNumber] == r.blockRestoreGeneration {
			_, err := io.WriteString(out, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			return err
		}
	}
	return r.base.RunWithEnv(ctx, env, args, in, out, errout)
}
