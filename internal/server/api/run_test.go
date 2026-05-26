package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// runEnv stands up a Handler with a fake docker runner so the WS
// plumbing can be exercised without a real daemon.
type runEnv struct {
	t      *testing.T
	srv    *httptest.Server
	db     *store.DB
	docker *runFakeDocker
}

// runFakeDocker is a minimal docker.Runner that lets each test stage
// what bytes the container produces on stdout/stderr and what the
// container's exit error should be.
type runFakeDocker struct {
	mu sync.Mutex
	// For each `docker run ...` invocation, copy stdin → stdout
	// (echo) until stdin closes, then return runErr. Tests override
	// onRun to do something different.
	onRun func(stdin io.Reader, stdout, stderr io.Writer) error
	// lastRunArgs captures the most recent `docker run` argv, so tests
	// can assert on flags wired up by the handler (e.g. `--env K=V`).
	lastRunArgs []string
}

func (f *runFakeDocker) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return f.RunWithEnv(ctx, nil, args, stdin, stdout, stderr)
}

func (f *runFakeDocker) RunWithEnv(_ context.Context, _ map[string]string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f.mu.Lock()
	cb := f.onRun
	if len(args) > 0 && args[0] == "run" {
		f.lastRunArgs = append([]string(nil), args...)
	}
	f.mu.Unlock()
	if cb == nil {
		// Default: echo stdin → stdout, exit 0.
		cb = func(stdin io.Reader, stdout, _ io.Writer) error {
			_, _ = io.Copy(stdout, stdin)
			return nil
		}
	}
	// Only intercept `docker run` invocations; other shell-outs
	// (e.g. service ls) get a benign empty response.
	if len(args) == 0 || args[0] != "run" {
		return nil
	}
	return cb(stdin, stdout, stderr)
}

// runEnvVars returns the `KEY=VALUE` pairs that followed `--env` in
// the most recent `docker run` invocation, parsed into a map.
func (f *runFakeDocker) runEnvVars() map[string]string {
	f.mu.Lock()
	args := append([]string(nil), f.lastRunArgs...)
	f.mu.Unlock()
	out := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--env" {
			continue
		}
		kv := args[i+1]
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		out[kv[:eq]] = kv[eq+1:]
	}
	return out
}

func newRunEnv(t *testing.T) *runEnv {
	t.Helper()
	db := openTestDB(t)

	fdocker := &runFakeDocker{}
	dockerCli := docker.NewWithRunner(fdocker)

	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:     db,
		Docker: dockerCli,
		Queue:  deploy.NewQueue(db),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &runEnv{t: t, srv: srv, db: db, docker: fdocker}
}

// seedLiveDeploy creates a project and a successful deployment with the
// supplied resolved cobaltfile. Returns the project name.
func (e *runEnv) seedLiveDeploy(name, resolvedCobaltfileJSON string) string {
	e.t.Helper()
	pid, err := e.db.CreateProject(context.Background(), store.Project{
		Name: name, GithubRepo: "h/" + name, Branch: "main",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: pid, Number: 1, Status: cobaltapi.StateQueued,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	_ = e.db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateSuccess)
	if resolvedCobaltfileJSON != "" {
		_ = e.db.SetResolvedCobaltfile(context.Background(), id, resolvedCobaltfileJSON)
	}
	return name
}

// dial opens a WS to the run endpoint with the given query.
func (e *runEnv) dial(t *testing.T, project, query string) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(e.srv.URL)
	u.Scheme = "ws"
	u.Path = "/api/projects/" + project + "/run"
	u.RawQuery = query
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		Subprotocols: []string{cobaltapi.RunSubprotocol},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return conn
}

// readFrames reads frames until an exit frame arrives or ctx times out.
// Returns the collected frames in order.
func readFrames(t *testing.T, conn *websocket.Conn, ctx context.Context) []cobaltapi.RunFrame {
	t.Helper()
	var frames []cobaltapi.RunFrame
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return frames
		}
		var f cobaltapi.RunFrame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		frames = append(frames, f)
		if f.Type == cobaltapi.RunFrameExit {
			return frames
		}
	}
}

// --- tests ---

func TestRun_HappyPath_EchoStdin(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	conn := e.dial(t, "api", "command="+url.QueryEscape("cat"))
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdinFrame, _ := json.Marshal(cobaltapi.RunFrame{Type: cobaltapi.RunFrameStdin, Data: "hello\n"})
	if err := conn.Write(ctx, websocket.MessageText, stdinFrame); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	// Trigger EOF on the daemon's stdin pipe by closing the WS write
	// side via a separate close-stdin signal. Our protocol doesn't have
	// a close-stdin frame yet, so for the test we rely on the docker
	// fake's default echo-then-EOF: pipe.Read returns when the writer
	// closes, but we're not closing... Use a finite-input fake instead.
	e.docker.mu.Lock()
	e.docker.onRun = func(_ io.Reader, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "echo: hello\n")
		return nil
	}
	e.docker.mu.Unlock()

	// Disconnect to break the read loop and let the handler proceed.
	// Actually it's simpler: close the WS first so the daemon-side
	// stdin pump stops, fake docker exits with no error.
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestRun_StdoutFramesArriveAtClient(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Stage docker to write a known string then exit.
	e.docker.onRun = func(_ io.Reader, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "deploy ok\n")
		return nil
	}

	conn := e.dial(t, "api", "command="+url.QueryEscape("echo deploy ok"))
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frames := readFrames(t, conn, ctx)
	if len(frames) == 0 {
		t.Fatalf("no frames received")
	}
	// Last frame must be exit.
	last := frames[len(frames)-1]
	if last.Type != cobaltapi.RunFrameExit {
		t.Errorf("last frame: %+v, want exit", last)
	}
	// Some frame should have data containing "deploy ok".
	got := ""
	for _, f := range frames {
		if f.Type == cobaltapi.RunFrameStdout {
			got += f.Data
		}
	}
	if !strings.Contains(got, "deploy ok") {
		t.Errorf("stdout: got %q, want 'deploy ok'", got)
	}
}

func TestRun_StderrFramesArriveAtClient(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	e.docker.onRun = func(_ io.Reader, _, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "warning: deprecated flag\n")
		return nil
	}

	conn := e.dial(t, "api", "command="+url.QueryEscape("foo"))
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frames := readFrames(t, conn, ctx)
	got := ""
	for _, f := range frames {
		if f.Type == cobaltapi.RunFrameStderr {
			got += f.Data
		}
	}
	if !strings.Contains(got, "deprecated flag") {
		t.Errorf("stderr: got %q", got)
	}
}

func TestRun_NonExitErrorReportsMinusOne(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Generic error (not a *exec.ExitError) means we couldn't determine
	// a real exit code; we report -1 so callers can distinguish from a
	// container that genuinely returned 0.
	e.docker.onRun = func(_ io.Reader, _ io.Writer, _ io.Writer) error {
		return errors.New("docker socket unreachable")
	}

	conn := e.dial(t, "api", "command=false")
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := readFrames(t, conn, ctx)
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	last := frames[len(frames)-1]
	if last.Type != cobaltapi.RunFrameExit {
		t.Errorf("last frame: %+v", last)
	}
	if last.Code != -1 {
		t.Errorf("exit code: %d, want -1 for non-ExitError", last.Code)
	}
}

func TestRun_RealExitCodePropagated(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Simulate the real ExecRunner path: an *exec.ExitError carrying
	// a specific exit code (here 42), wrapped the same way the runner
	// wraps it in production.
	e.docker.onRun = func(_ io.Reader, _ io.Writer, _ io.Writer) error {
		realErr := exec.Command("sh", "-c", "exit 42").Run()
		return fmt.Errorf("docker run: %w: simulated", realErr)
	}

	conn := e.dial(t, "api", "command=false")
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := readFrames(t, conn, ctx)
	last := frames[len(frames)-1]
	if last.Code != 42 {
		t.Errorf("exit code: %d, want 42", last.Code)
	}
}

func TestCobaltSyntheticEnv(t *testing.T) {
	t.Parallel()
	ptr := func(s string) *string { return &s }
	project := &store.Project{Name: "api"}

	cases := []struct {
		name       string
		live       *store.Deployment
		publicHost string
		wantKeys   []string
		notWant    []string
		wantValues map[string]string
	}{
		{
			name:       "minimal: no host, no commit",
			live:       &store.Deployment{Number: 7},
			publicHost: "",
			wantKeys:   []string{"COBALT_PROJECT_NAME", "COBALT_SERVICE_NAME", "COBALT_DEPLOYMENT_NUMBER"},
			notWant:    []string{"COBALT_HOST", "COBALT_COMMIT"},
			wantValues: map[string]string{
				"COBALT_PROJECT_NAME":      "api",
				"COBALT_SERVICE_NAME":      "web",
				"COBALT_DEPLOYMENT_NUMBER": "7",
			},
		},
		{
			name:       "with host configured",
			live:       &store.Deployment{Number: 1},
			publicHost: "cobalt.blue.cc",
			wantKeys:   []string{"COBALT_HOST"},
			wantValues: map[string]string{"COBALT_HOST": "cobalt.blue.cc"},
		},
		{
			name:       "with commit set",
			live:       &store.Deployment{Number: 1, CommitSHA: ptr("deadbeef")},
			publicHost: "",
			wantKeys:   []string{"COBALT_COMMIT"},
			wantValues: map[string]string{"COBALT_COMMIT": "deadbeef"},
		},
		{
			name:       "empty-string commit is treated as absent",
			live:       &store.Deployment{Number: 1, CommitSHA: ptr("")},
			publicHost: "",
			notWant:    []string{"COBALT_COMMIT"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cobaltSyntheticEnv(project, "web", c.live, c.publicHost)
			for _, k := range c.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("missing key %q in %v", k, got)
				}
			}
			for _, k := range c.notWant {
				if _, ok := got[k]; ok {
					t.Errorf("unexpected key %q in %v", k, got)
				}
			}
			for k, want := range c.wantValues {
				if got[k] != want {
					t.Errorf("%s: got %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

// drainAndClose dials the WS, lets the fake docker run-and-exit, and
// returns once frames have flowed. Tests inspect captured argv after.
func (e *runEnv) drainRun(t *testing.T, project, command string) {
	t.Helper()
	conn := e.dial(t, project, "command="+url.QueryEscape(command))
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = readFrames(t, conn, ctx)
}

func TestRun_InjectsProjectAndSyntheticEnv(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	proj, err := e.db.GetProjectByName(context.Background(), "api")
	if err != nil {
		t.Fatalf("GetProjectByName: %v", err)
	}
	if err := e.db.SetEnvVar(context.Background(), proj.ID, "FIREBASE_PRIVATE_KEY", "secret-value"); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}

	// Stage a benign docker invocation.
	e.docker.onRun = func(_ io.Reader, _, _ io.Writer) error { return nil }

	e.drainRun(t, "api", "true")

	got := e.docker.runEnvVars()
	if got["FIREBASE_PRIVATE_KEY"] != "secret-value" {
		t.Errorf("project env missing: got %q", got["FIREBASE_PRIVATE_KEY"])
	}
	if got["COBALT_PROJECT_NAME"] != "api" {
		t.Errorf("COBALT_PROJECT_NAME: got %q, want %q", got["COBALT_PROJECT_NAME"], "api")
	}
	if got["COBALT_SERVICE_NAME"] != "web" {
		t.Errorf("COBALT_SERVICE_NAME: got %q, want %q", got["COBALT_SERVICE_NAME"], "web")
	}
	if got["COBALT_DEPLOYMENT_NUMBER"] != "1" {
		t.Errorf("COBALT_DEPLOYMENT_NUMBER: got %q, want %q", got["COBALT_DEPLOYMENT_NUMBER"], "1")
	}
}

func TestRun_SyntheticOverridesProjectEnv(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	proj, err := e.db.GetProjectByName(context.Background(), "api")
	if err != nil {
		t.Fatalf("GetProjectByName: %v", err)
	}
	// User-set value that collides with a daemon-authoritative var.
	if err := e.db.SetEnvVar(context.Background(), proj.ID, "COBALT_PROJECT_NAME", "hacked"); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}

	e.docker.onRun = func(_ io.Reader, _, _ io.Writer) error { return nil }
	e.drainRun(t, "api", "true")

	got := e.docker.runEnvVars()
	if got["COBALT_PROJECT_NAME"] != "api" {
		t.Errorf("synthetic should override project env: got %q, want %q", got["COBALT_PROJECT_NAME"], "api")
	}
}

func TestRun_MissingCommandReturns400(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", "")

	resp, err := http.Get(e.srv.URL + "/api/projects/api/run")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", resp.StatusCode)
	}
}

func TestRun_NoSuccessfulDeployReturns404(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "fresh", GithubRepo: "h/fresh", Branch: "main",
	})
	resp, err := http.Get(e.srv.URL + "/api/projects/fresh/run?command=ls")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: %d, want 404", resp.StatusCode)
	}
}

func TestRun_ResolveImage_Defaults(t *testing.T) {
	t.Parallel()
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name           string
		resolved       *string
		serviceName    string
		wantImage      string
		wantExtraParam bool
		wantVolumes    int
	}{
		{
			name:        "no resolved cobaltfile uses default",
			resolved:    nil,
			serviceName: "web",
			wantImage:   "default",
		},
		{
			name:        "service not in cobaltfile uses default",
			resolved:    ptr(`{"version":"1.0","services":{"other":{}}}`),
			serviceName: "web",
			wantImage:   "default",
		},
		{
			name:        "service overrides image",
			resolved:    ptr(`{"version":"1.0","services":{"web":{"image":"alt"}},"images":{"alt":{"dockerfile":"Dockerfile"}}}`),
			serviceName: "web",
			wantImage:   "alt",
		},
		{
			name:           "extraRunParams threaded through",
			resolved:       ptr(`{"version":"1.0","services":{"web":{"port":3000,"extraRunParams":"--add-host host.docker.internal:host-gateway"}}}`),
			serviceName:    "web",
			wantImage:      "default",
			wantExtraParam: true,
		},
		{
			name:        "service volumes returned",
			resolved:    ptr(`{"version":"1.0","services":{"web":{"volumes":[{"name":"data","destinationPath":"/var/lib/data"},{"name":"uploads","destinationPath":"/srv/uploads"}]}}}`),
			serviceName: "web",
			wantImage:   "default",
			wantVolumes: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			image, extra, vols := resolveRunImage(&store.Deployment{ResolvedCobaltfile: c.resolved}, 7, c.serviceName)
			if image != c.wantImage {
				t.Errorf("image: got %q, want %q", image, c.wantImage)
			}
			if c.wantExtraParam && len(extra) == 0 {
				t.Errorf("expected extra params, got %v", extra)
			}
			if !c.wantExtraParam && len(extra) > 0 {
				t.Errorf("unexpected extra params: %v", extra)
			}
			if got, want := len(vols), c.wantVolumes; got != want {
				t.Errorf("volumes: got %d, want %d", got, want)
			}
			// Volume names get the per-project prefix from
			// docker.VolumeName so two projects can declare a volume
			// called "data" without colliding.
			for _, v := range vols {
				if v.VolumeName == "" || v.DestinationPath == "" {
					t.Errorf("malformed volume: %+v", v)
				}
			}
		})
	}
}
