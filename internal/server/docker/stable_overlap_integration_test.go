//go:build integration

package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStablePublicWebReplicaOverlap_RealSwarm measures live processes rather
// than inferring capacity from desired replicas or update parallelism.
func TestStablePublicWebReplicaOverlap_RealSwarm(t *testing.T) {
	if os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires caller-owned disposable Swarm")
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" || strings.HasPrefix(host, "unix:") || os.Getenv("COBALT_DISPOSABLE_SWARM_OWNERSHIP") != "fix-stable-private-web" {
		t.Fatal("explicit owned disposable DOCKER_HOST required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if run("info", "--format", "{{.Swarm.LocalNodeState}}") != "active" {
		t.Fatal("caller must initialize owned disposable Swarm")
	}
	id := time.Now().UnixNano()
	name := StablePublicWebServiceName(id)
	network := fmt.Sprintf("overlap-%d", id)
	run("pull", "python:3.12-alpine")
	run("network", "create", "--driver", "overlay", "--attachable", network)
	t.Cleanup(func() {
		_ = exec.Command("docker", "service", "rm", name).Run()
		_ = exec.Command("docker", "network", "rm", network).Run()
	})
	command := func(version string) string {
		source := fmt.Sprintf(`import http.server,signal,threading,time,pathlib
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  if self.path=='/slow': pathlib.Path('/tmp/slow-started').write_text('yes'); time.sleep(30)
  self.send_response(200); self.end_headers(); self.wfile.write(%q.encode())
 def log_message(self,*a): pass
server=http.server.ThreadingHTTPServer(('0.0.0.0',8080),H)
def stop(*a):
 def drain(): time.sleep(30); server.shutdown()
 threading.Thread(target=drain,daemon=True).start()
signal.signal(signal.SIGTERM,stop)
server.serve_forever()
server.server_close()
`, version)
		return "python -u -c 'import base64;exec(base64.b64decode(\"" + base64.StdEncoding.EncodeToString([]byte(source)) + "\"))'"
	}
	c := New()
	opts := ServiceCreateOpts{ProjectID: id, ProjectName: "overlap", ServiceName: "web", DeploymentNumber: 1, Name: name, Image: "python:3.12-alpine", Command: command("old"), Replicas: 4, Networks: []NetworkAttachment{{Name: network, Alias: name}}, StopGracePeriod: 35 * time.Second, HealthCommand: `python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8080",timeout=2)'`}
	if err := c.ReconcileStableService(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitForServiceDeploymentHealthy(ctx, name, 1, 4, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	oldIDs := strings.Fields(run("ps", "--filter", "label=com.docker.swarm.service.name="+name, "--no-trunc", "--format", "{{.ID}}"))
	if len(oldIDs) != 4 {
		t.Fatalf("initial actual containers=%v", oldIDs)
	}
	type request struct {
		cmd  *exec.Cmd
		body bytes.Buffer
	}
	requests := make([]*request, 0, 4)
	for _, container := range oldIDs {
		req := &request{cmd: exec.CommandContext(ctx, "docker", "exec", container, "python", "-c", "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080/slow',timeout=60).read().decode())")}
		req.cmd.Stdout = &req.body
		req.cmd.Stderr = &req.body
		if err := req.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, req)
	}
	for _, container := range oldIDs {
		deadline := time.Now().Add(10 * time.Second)
		for {
			_, err := exec.CommandContext(ctx, "docker", "exec", container, "test", "-f", "/tmp/slow-started").CombinedOutput()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("long request never reached old task")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	type measurement struct {
		Replicas        int `json:"replicas"`
		GraceSeconds    int `json:"stopGraceSeconds"`
		HandlerSeconds  int `json:"handlerSeconds"`
		Samples         int `json:"samples"`
		MaxContainers   int `json:"maxActualRunningContainers"`
		MaxLiveTasks    int `json:"maxActualRunningTasks"`
		MaxDraining     int `json:"maxDesiredShutdownStillRunning"`
		FinalContainers int `json:"finalActualContainers"`
	}
	result := measurement{Replicas: 4, GraceSeconds: 35, HandlerSeconds: 30}
	var mu sync.Mutex
	sample := func() error {
		out, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=com.docker.swarm.service.name="+name, "--format", "{{.ID}}").Output()
		if err != nil {
			return err
		}
		containers := len(strings.Fields(string(out)))
		out, err = exec.CommandContext(ctx, "docker", "service", "ps", name, "--no-trunc", "--quiet").Output()
		if err != nil {
			return err
		}
		ids := strings.Fields(string(out))
		if len(ids) == 0 {
			return fmt.Errorf("missing manager tasks")
		}
		out, err = exec.CommandContext(ctx, "docker", append([]string{"inspect", "--type", "task"}, ids...)...).Output()
		if err != nil {
			return err
		}
		var tasks []struct {
			Status       struct{ State string }
			DesiredState string
		}
		if err := json.Unmarshal(out, &tasks); err != nil {
			return err
		}
		live, draining := 0, 0
		for _, task := range tasks {
			if task.Status.State == "running" {
				live++
				if task.DesiredState == "shutdown" {
					draining++
				}
			}
		}
		mu.Lock()
		result.Samples++
		result.MaxContainers = max(result.MaxContainers, containers)
		result.MaxLiveTasks = max(result.MaxLiveTasks, live)
		result.MaxDraining = max(result.MaxDraining, draining)
		mu.Unlock()
		return nil
	}
	if err := sample(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	sampled := make(chan struct{})
	sampleErrors := make(chan error, 1)
	go func() {
		defer close(sampled)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-ticker.C:
				if err := sample(); err != nil {
					select {
					case sampleErrors <- err:
					default:
					}
					return
				}
			}
		}
	}()

	eventCtx, eventCancel := context.WithCancel(ctx)
	events := exec.CommandContext(eventCtx, "docker", "events", "--since", fmt.Sprint(time.Now().Unix()), "--filter", "type=container", "--filter", "label=com.docker.swarm.service.name="+name, "--filter", "event=start", "--filter", "event=die", "--format", "{{json .}}")
	eventPipe, err := events.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	eventDone := make(chan struct{})
	eventErr := make(chan error, 1)
	liveEvents := map[string]bool{}
	for _, id := range oldIDs {
		liveEvents[id] = true
	}
	maxEventContainers, eventStarts, eventDeaths := 4, 0, 0
	go func() {
		defer close(eventDone)
		scanner := bufio.NewScanner(eventPipe)
		for scanner.Scan() {
			var event struct {
				Action string
				Actor  struct{ ID string }
			}
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				eventErr <- err
				return
			}
			mu.Lock()
			if event.Action == "start" && !liveEvents[event.Actor.ID] {
				liveEvents[event.Actor.ID] = true
				eventStarts++
			}
			if event.Action == "die" && liveEvents[event.Actor.ID] {
				delete(liveEvents, event.Actor.ID)
				eventDeaths++
			}
			maxEventContainers = max(maxEventContainers, len(liveEvents))
			mu.Unlock()
		}
		if err := scanner.Err(); err != nil {
			eventErr <- err
		}
	}()
	defer func() { eventCancel(); _ = events.Wait(); <-eventDone }()
	opts.DeploymentNumber = 2
	opts.Command = command("new")
	updateErr := c.ReconcileStableService(ctx, opts)
	if updateErr == nil {
		updateErr = c.WaitForServiceDeploymentHealthy(ctx, name, 2, 4, 4*time.Minute)
	}
	if updateErr == nil {
		updateErr = c.WaitForStableServiceSettled(ctx, name)
	}
	close(finished)
	<-sampled
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	select {
	case err := <-sampleErrors:
		t.Fatal(err)
	default:
	}
	for _, req := range requests {
		if err := req.cmd.Wait(); err != nil || strings.TrimSpace(req.body.String()) != "old" {
			t.Fatalf("30-second old request: %v %s", err, req.body.String())
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		complete := eventStarts == 4 && eventDeaths == 4 && len(liveEvents) == 4
		mu.Unlock()
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("incomplete actual container start/die event stream")
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	t.Logf("ACTUAL_CONTAINER_EVENTS maxLive=%d starts=%d deaths=%d finalLive=%d", maxEventContainers, eventStarts, eventDeaths, len(liveEvents))
	mu.Unlock()
	select {
	case err := <-eventErr:
		t.Fatal(err)
	default:
	}
	mu.Lock()
	result.FinalContainers = len(strings.Fields(run("ps", "--filter", "label=com.docker.swarm.service.name="+name, "--format", "{{.ID}}")))
	report, _ := json.Marshal(result)
	mu.Unlock()
	t.Logf("ACTUAL_PROCESS_OVERLAP %s", report)
	if result.Samples < 10 || result.MaxContainers < 5 || result.MaxLiveTasks < 5 || result.MaxDraining < 1 || result.FinalContainers != 4 {
		t.Fatalf("insufficient/incomplete actual-process measurement: %s", report)
	}
	if got := run("service", "inspect", name, "--format", "{{.Spec.UpdateConfig.Parallelism}} {{.Spec.UpdateConfig.Order}}"); got != "1 start-first" {
		t.Fatalf("wrong measured policy: %s", got)
	}
}
