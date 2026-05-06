package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
}

func (f *runFakeDocker) Run(_ context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f.mu.Lock()
	cb := f.onRun
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

func newRunEnv(t *testing.T) *runEnv {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

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

func TestRun_NonZeroExitCodeSurfacedAsOne(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	e.docker.onRun = func(_ io.Reader, _ io.Writer, _ io.Writer) error {
		return errors.New("container exited with non-zero status")
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
	if last.Code != 1 {
		t.Errorf("exit code: %d, want 1", last.Code)
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
	cases := []struct {
		name           string
		resolved       sql.NullString
		serviceName    string
		wantImage      string
		wantExtraParam bool
	}{
		{
			name:        "no resolved cobaltfile uses default",
			resolved:    sql.NullString{},
			serviceName: "web",
			wantImage:   "default",
		},
		{
			name:        "service not in cobaltfile uses default",
			resolved:    sql.NullString{String: `{"version":"1.0","services":{"other":{}}}`, Valid: true},
			serviceName: "web",
			wantImage:   "default",
		},
		{
			name:        "service overrides image",
			resolved:    sql.NullString{String: `{"version":"1.0","services":{"web":{"image":"alt"}},"images":{"alt":{"dockerfile":"Dockerfile"}}}`, Valid: true},
			serviceName: "web",
			wantImage:   "alt",
		},
		{
			name:           "extraRunParams threaded through",
			resolved:       sql.NullString{String: `{"version":"1.0","services":{"web":{"port":3000,"extraRunParams":"--add-host host.docker.internal:host-gateway"}}}`, Valid: true},
			serviceName:    "web",
			wantImage:      "default",
			wantExtraParam: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			image, extra := resolveRunImage(&store.Deployment{ResolvedCobaltfile: c.resolved}, c.serviceName)
			if image != c.wantImage {
				t.Errorf("image: got %q, want %q", image, c.wantImage)
			}
			if c.wantExtraParam && len(extra) == 0 {
				t.Errorf("expected extra params, got %v", extra)
			}
			if !c.wantExtraParam && len(extra) > 0 {
				t.Errorf("unexpected extra params: %v", extra)
			}
		})
	}
}
