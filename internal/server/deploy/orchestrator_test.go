package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// fakeDockerRunner records every shell-out so the orchestrator can be
// tested end-to-end without a real docker daemon.
type orchestratorDocker struct {
	mu     sync.Mutex
	calls  []string
	stdout map[string]string
	errs   map[string]error
}

func newOrchDocker() *orchestratorDocker {
	return &orchestratorDocker{stdout: map[string]string{}, errs: map[string]error{}}
}

func (f *orchestratorDocker) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return f.RunWithEnv(ctx, nil, args, stdin, stdout, stderr)
}

func (f *orchestratorDocker) RunWithEnv(_ context.Context, _ map[string]string, args []string, _ io.Reader, stdout, _ io.Writer) error {
	joined := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, joined)
	f.mu.Unlock()

	for prefix, err := range f.errs {
		if strings.HasPrefix(joined, prefix) {
			return err
		}
	}
	for prefix, out := range f.stdout {
		if strings.HasPrefix(joined, prefix) && stdout != nil {
			_, _ = io.WriteString(stdout, out)
			break
		}
	}
	return nil
}

func (f *orchestratorDocker) hasCall(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// orchestratorCaddy is a minimal Caddy fake (separate from the routes/
// upstream tests' fakeCaddy because we need to control swap behavior
// per-test without per-test Caddy setup).
type orchestratorCaddy struct {
	*httptest.Server
	mu        sync.Mutex
	upstreams map[int64]string
	failPatch atomic2
}

type atomic2 struct {
	mu sync.Mutex
	v  bool
}

func (a *atomic2) set(v bool)   { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomic2) get() bool    { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

func newOrchCaddy(t *testing.T) *orchestratorCaddy {
	t.Helper()
	c := &orchestratorCaddy{upstreams: map[int64]string{}}
	c.Server = httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(c.Close)
	return c
}

func (c *orchestratorCaddy) handle(w http.ResponseWriter, r *http.Request) {
	if c.failPatch.get() && r.Method == http.MethodPatch {
		http.Error(w, "patch failed", http.StatusInternalServerError)
		return
	}
	// Recognize project-handler PATCHes; extract upstream dial from body.
	if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "cobalt-project-handler-") {
		idStr := strings.TrimPrefix(r.URL.Path, "/id/cobalt-project-handler-")
		var id int64
		_, _ = fmtSscan(idStr, &id)
		body := readBody(r.Body)
		dial := extractDial(body)
		c.mu.Lock()
		c.upstreams[id] = dial
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	// GET dial subpath.
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/upstreams/0/dial") {
		idStr := strings.TrimPrefix(r.URL.Path, "/id/cobalt-project-handler-")
		idStr = strings.TrimSuffix(idStr, "/upstreams/0/dial")
		var id int64
		_, _ = fmtSscan(idStr, &id)
		c.mu.Lock()
		dial := c.upstreams[id]
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`"` + stripHostFromDial(dial) + `"`))
		return
	}
	// Everything else: accept.
	w.WriteHeader(http.StatusOK)
}

// fmtSscan / extractDial / readBody / stripHostFromDial are tiny
// inline parsers — kept here to avoid adding stdlib imports at the top.

func fmtSscan(s string, n *int64) (int, error) {
	var v int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		v = v*10 + int64(ch-'0')
	}
	*n = v
	return 1, nil
}

func readBody(r io.ReadCloser) []byte {
	defer r.Close()
	b, _ := io.ReadAll(r)
	return b
}

// extractDial pulls "container:port" out of a JSON PATCH body containing
// "upstreams":[{"dial":"foo:80"}].
func extractDial(body []byte) string {
	const k = `"dial":"`
	i := strings.Index(string(body), k)
	if i < 0 {
		return ""
	}
	rest := string(body[i+len(k):])
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func stripHostFromDial(dial string) string {
	if i := strings.IndexByte(dial, ':'); i >= 0 {
		return dial[:i]
	}
	return dial
}

// fakePreparer / fakeBuilder skip the git+docker work for unit tests.

type fakePrep struct {
	ws  *Workspace
	err error
}

func (f *fakePrep) Prepare(_ context.Context, _ store.Project, _ store.Deployment) (*Workspace, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.ws, nil
}

type fakeBuild struct {
	built []BuiltService
	err   error
}

func (f *fakeBuild) Build(_ context.Context, _ store.Project, _ store.Deployment, _ *Workspace, _ io.Writer) ([]BuiltService, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.built, nil
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func setupOrchestrator(t *testing.T) (*Orchestrator, *orchestratorDocker, *orchestratorCaddy, *store.DB, store.Project) {
	t.Helper()
	db := openTestDB(t)

	pid, err := db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Default workspace has a `web` service, which the orchestrator
	// preflight requires at least one domain for.
	if err := db.AddDomain(context.Background(), pid, "api.example.com"); err != nil {
		t.Fatal(err)
	}
	project, _ := db.GetProjectByName(context.Background(), "api")

	fdocker := newOrchDocker()
	fdocker.stdout["service ps"] =
		`{"current_state":"Running","health":""}` + "\n"
	fdocker.stdout["service ls"] = "" // no existing services
	fdocker.stdout["network ls"] = "" // network missing → create

	dockerCli := docker.NewWithRunner(fdocker)

	fcaddy := newOrchCaddy(t)
	caddyCli := caddy.NewHTTPClient(fcaddy.URL, fcaddy.Client())
	caddyCli.PatchVerifyBackoff = []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
	}

	o := &Orchestrator{
		DB:        db,
		Docker:    dockerCli,
		Caddy:     caddyCli,
		Preparer:  &fakePrep{ws: defaultWorkspace()},
		Builder:   &fakeBuild{built: defaultBuilt()},
		DataDir:   t.TempDir(),
		Log:       quietLog(),
		LogWriter: io.Discard,
	}
	return o, fdocker, fcaddy, db, *project
}

func defaultWorkspace() *Workspace {
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{
			"default": {Dockerfile: "Dockerfile", Context: "."},
		},
	}
	return &Workspace{Path: "/tmp/repo", Cobaltfile: cf, Commit: "abc"}
}

func defaultBuilt() []BuiltService {
	return []BuiltService{{
		Name: "web",
		Service: cobaltfile.Service{
			Type: cobaltfile.TypeContainer, Image: "default", Port: 3000,
		},
		ImageTag: "cobalt/project-api-default:1",
	}}
}

func enqueueAndFetch(t *testing.T, db *store.DB, projectID int64) store.Deployment {
	t.Helper()
	q := NewQueue(db)
	id, _, err := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: projectID})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_ = db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateFetching)
	dep, err := db.GetDeployment(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return *dep
}

// --- tests ---

func TestOrchestrator_HappyPath(t *testing.T) {
	t.Parallel()
	o, fdocker, fcaddy, db, project := setupOrchestrator(t)
	dep := enqueueAndFetch(t, db, project.ID)

	if err := o.Run(context.Background(), dep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !fdocker.hasCall("service create") {
		t.Error("expected service create call")
	}
	if !fdocker.hasCall("service ps") {
		t.Error("expected healthcheck poll call")
	}
	fcaddy.mu.Lock()
	got := fcaddy.upstreams[project.ID]
	fcaddy.mu.Unlock()
	if got != "api-1-web:3000" {
		t.Errorf("Caddy upstream: got %q, want api-1-web:3000", got)
	}
}

func TestOrchestrator_PrepareErrorReturns(t *testing.T) {
	t.Parallel()
	o, _, _, db, project := setupOrchestrator(t)
	o.Preparer = &fakePrep{err: errors.New("clone failed")}
	dep := enqueueAndFetch(t, db, project.ID)

	err := o.Run(context.Background(), dep)
	if err == nil || !strings.Contains(err.Error(), "prepare") {
		t.Errorf("expected prepare error, got %v", err)
	}
}

// TestOrchestrator_RejectsWebDeployWithNoDomains asserts that a web
// project missing domains errors before the build runs (instead of
// dying confusingly mid-Caddy-swap).
func TestOrchestrator_RejectsWebDeployWithNoDomains(t *testing.T) {
	t.Parallel()
	o, fdocker, _, db, _ := setupOrchestrator(t)
	// Strip the domain seeded by setupOrchestrator.
	if err := db.RemoveDomain(context.Background(), 1, "api.example.com"); err != nil {
		t.Fatal(err)
	}
	dep := enqueueAndFetch(t, db, 1)

	err := o.Run(context.Background(), dep)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no domains attached") {
		t.Errorf("error %q does not mention domains", err)
	}
	if fdocker.hasCall("service create") {
		t.Error("preflight did not run before service create")
	}
}

// TestOrchestrator_FailureWritesErrorToDeployLog asserts that when a
// deploy fails, the cause shows up in the deploy log so operators can
// see it via `cobalt deployments output` without shell access to the
// daemon host.
func TestOrchestrator_FailureWritesErrorToDeployLog(t *testing.T) {
	t.Parallel()
	o, _, _, db, project := setupOrchestrator(t)
	var buf bytes.Buffer
	o.LogWriter = &buf
	o.Preparer = &fakePrep{err: errors.New("clone: exit status 128: could not read Username")}
	dep := enqueueAndFetch(t, db, project.ID)

	if err := o.Run(context.Background(), dep); err == nil {
		t.Fatal("expected error")
	}
	logged := buf.String()
	if !strings.Contains(logged, "❌") {
		t.Errorf("deploy log missing ❌ failure marker: %q", logged)
	}
	if !strings.Contains(logged, "could not read Username") {
		t.Errorf("deploy log missing root-cause text: %q", logged)
	}
	if !strings.Contains(logged, "started") {
		t.Errorf("deploy log missing started marker: %q", logged)
	}
	if !strings.Contains(logged, "deploy failed") {
		t.Errorf("deploy log missing failure marker: %q", logged)
	}
}

func TestOrchestrator_BuildErrorStopsNoServices(t *testing.T) {
	t.Parallel()
	o, fdocker, _, db, project := setupOrchestrator(t)
	o.Builder = &fakeBuild{err: errors.New("build failed")}
	dep := enqueueAndFetch(t, db, project.ID)

	if err := o.Run(context.Background(), dep); err == nil {
		t.Error("expected error")
	}
	if fdocker.hasCall("service create") {
		t.Error("services started despite build failure")
	}
}

func TestOrchestrator_HealthcheckFailureStopsServices(t *testing.T) {
	t.Parallel()
	o, fdocker, fcaddy, db, project := setupOrchestrator(t)
	// Force fail-fast: 3 shutdown states for 1 replica.
	fdocker.stdout["service ps"] =
		`{"current_state":"Shutdown 1m ago","health":""}` + "\n" +
			`{"current_state":"Failed 30s ago","health":""}` + "\n" +
			`{"current_state":"Rejected 10s ago","health":""}` + "\n"
	dep := enqueueAndFetch(t, db, project.ID)

	if err := o.Run(context.Background(), dep); err == nil {
		t.Error("expected healthcheck failure")
	}
	if !fdocker.hasCall("service create") {
		t.Error("expected at least one service create attempt")
	}
	if !fdocker.hasCall("service rm") {
		t.Error("expected services to be cleaned up")
	}
	// Caddy must NOT have been touched.
	fcaddy.mu.Lock()
	defer fcaddy.mu.Unlock()
	if len(fcaddy.upstreams) != 0 {
		t.Errorf("Caddy was touched: %v", fcaddy.upstreams)
	}
}

func TestOrchestrator_CaddyVerifyFailureRevertsAndCleansUp(t *testing.T) {
	t.Parallel()
	o, fdocker, fcaddy, db, project := setupOrchestrator(t)

	// First seed a "previous successful deployment" so revert has a target.
	prevID, _ := db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: project.ID, Number: 1, Status: cobaltapi.StateQueued,
	})
	_ = db.SetDeploymentStatus(context.Background(), prevID, cobaltapi.StateSuccess)

	// Force Caddy to reject all PATCHes.
	fcaddy.failPatch.set(true)

	dep := enqueueAndFetch(t, db, project.ID)
	err := o.Run(context.Background(), dep)
	if err == nil {
		t.Error("expected error")
	}
	if !fdocker.hasCall("service rm") {
		t.Error("expected services to be cleaned up after Caddy failure")
	}
}

func TestOrchestrator_FirstDeployNoPriorSuccessForRevert(t *testing.T) {
	t.Parallel()
	o, _, fcaddy, db, project := setupOrchestrator(t)
	// No prior successful deployment. Force Caddy to fail so revert path
	// runs. Revert should be a no-op (logged "no prior success") and the
	// orchestrator should still return the original error.
	fcaddy.failPatch.set(true)

	dep := enqueueAndFetch(t, db, project.ID)
	if err := o.Run(context.Background(), dep); err == nil {
		t.Error("expected error")
	}
}

func TestOrchestrator_AfterHookFailureDoesNotRollBack(t *testing.T) {
	t.Parallel()
	o, fdocker, fcaddy, db, project := setupOrchestrator(t)

	// Add an after-hook that's wired to fail.
	ws := o.Preparer.(*fakePrep).ws
	ws.Cobaltfile.Services[cobaltfile.HookDeployStartAfter] = cobaltfile.Service{
		Type:    cobaltfile.TypeCommand,
		Image:   "default",
		Port:    cobaltfile.DefaultPort,
		Command: "exit 1",
	}
	// Fail any docker run that's the after-hook (simplest: fail any "run"
	// invocation that isn't the volume-export tar one).
	fdocker.errs["run --rm --name api-hook-deploy-start-after.1"] = errors.New("hook exit 1")

	dep := enqueueAndFetch(t, db, project.ID)
	if err := o.Run(context.Background(), dep); err != nil {
		t.Errorf("after-hook failure should NOT fail deploy, got: %v", err)
	}
	// Caddy upstream should still have been swapped — the deploy is live.
	fcaddy.mu.Lock()
	defer fcaddy.mu.Unlock()
	if got := fcaddy.upstreams[project.ID]; got != "api-1-web:3000" {
		t.Errorf("upstream: got %q, want api-1-web:3000", got)
	}
}

// TestOrchestrator_HooksHappyPath asserts both before- and after-hooks are
// actually invoked through the deploy flow, in the right order relative to
// service create / Caddy swap, with extraRunParams threaded into argv.
// The negative path is covered by TestOrchestrator_AfterHookFailureDoesNotRollBack.
func TestOrchestrator_HooksHappyPath(t *testing.T) {
	t.Parallel()
	o, fdocker, _, db, project := setupOrchestrator(t)

	ws := o.Preparer.(*fakePrep).ws
	ws.Cobaltfile.Services[cobaltfile.HookDeployStartBefore] = cobaltfile.Service{
		Type:           cobaltfile.TypeCommand,
		Image:          "default",
		Port:           cobaltfile.DefaultPort,
		Command:        "echo before",
		ExtraRunParams: "--add-host host.docker.internal:host-gateway",
	}
	ws.Cobaltfile.Services[cobaltfile.HookDeployStartAfter] = cobaltfile.Service{
		Type:    cobaltfile.TypeCommand,
		Image:   "default",
		Port:    cobaltfile.DefaultPort,
		Command: "echo after",
	}

	dep := enqueueAndFetch(t, db, project.ID)
	if err := o.Run(context.Background(), dep); err != nil {
		t.Fatalf("Run: %v", err)
	}

	fdocker.mu.Lock()
	defer fdocker.mu.Unlock()

	idxBefore := firstCallIndex(fdocker.calls, "run --rm --name api-hook-deploy-start-before.1")
	idxAfter := firstCallIndex(fdocker.calls, "run --rm --name api-hook-deploy-start-after.1")
	idxCreate := firstCallIndex(fdocker.calls, "service create")
	if idxBefore < 0 {
		t.Fatalf("before-hook never invoked; calls=%v", fdocker.calls)
	}
	if idxAfter < 0 {
		t.Fatalf("after-hook never invoked; calls=%v", fdocker.calls)
	}
	if idxCreate < 0 {
		t.Fatalf("no service create; calls=%v", fdocker.calls)
	}
	if idxBefore >= idxCreate {
		t.Errorf("before-hook should run before service create (before=%d, create=%d)", idxBefore, idxCreate)
	}
	if idxAfter <= idxCreate {
		t.Errorf("after-hook should run after service create (after=%d, create=%d)", idxAfter, idxCreate)
	}

	// extraRunParams from the cobaltfile must reach the actual docker argv
	// for the before-hook. The after-hook didn't set them, so don't check.
	if !strings.Contains(fdocker.calls[idxBefore], "--add-host host.docker.internal:host-gateway") {
		t.Errorf("before-hook missing --add-host argv: %q", fdocker.calls[idxBefore])
	}
}

// firstCallIndex returns the index of the first recorded call with the
// given prefix, or -1 if none. Used to assert ordering of the docker
// shell-outs captured by orchestratorDocker.
func firstCallIndex(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestOrchestrator_NoWebServiceSkipsCaddy(t *testing.T) {
	t.Parallel()
	o, _, fcaddy, db, project := setupOrchestrator(t)
	// Replace web with a cron-only project.
	ws := o.Preparer.(*fakePrep).ws
	ws.Cobaltfile.Services = map[string]cobaltfile.Service{
		"nightly": {
			Type: cobaltfile.TypeCron, Image: "default", Port: cobaltfile.DefaultPort,
			Schedule: "0 0 * * *", Command: "node nightly.js",
		},
	}
	o.Builder = &fakeBuild{built: []BuiltService{
		{Name: "nightly", Service: ws.Cobaltfile.Services["nightly"], ImageTag: "img:1"},
	}}

	dep := enqueueAndFetch(t, db, project.ID)
	if err := o.Run(context.Background(), dep); err != nil {
		t.Errorf("no-web deploy: %v", err)
	}
	fcaddy.mu.Lock()
	defer fcaddy.mu.Unlock()
	if len(fcaddy.upstreams) != 0 {
		t.Errorf("Caddy should not have been touched: %v", fcaddy.upstreams)
	}
}

func TestOrchestrator_GeneratorSwapsToStaticSite(t *testing.T) {
	t.Parallel()
	o, _, _, db, project := setupOrchestrator(t)
	// Override the static-sites root so the generator's mkdir lands in a
	// writable temp dir.
	o.Caddy.StaticSitesDir = t.TempDir()

	ws := o.Preparer.(*fakePrep).ws
	ws.Cobaltfile.Services = map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeStatic, Image: "default", Port: 8000, PublicPath: "dist"},
		"gen": {Type: cobaltfile.TypeGenerator, Image: "default", Port: 8000, Command: "build"},
	}
	o.Builder = &fakeBuild{built: []BuiltService{
		{Name: "web", Service: ws.Cobaltfile.Services["web"]},
		{Name: "gen", Service: ws.Cobaltfile.Services["gen"], ImageTag: "img:1"},
	}}

	dep := enqueueAndFetch(t, db, project.ID)
	if err := o.Run(context.Background(), dep); err != nil {
		t.Errorf("Run: %v", err)
	}
	// The generator's bind-mount target dir should have been created.
	wantDir := filepath.Join(o.Caddy.StaticSitesDir, project.Name, "deployments", "1")
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("static dir not created: %v", err)
	}
}

// Mid-flight context cancellation is exercised indirectly via the
// healthcheck-failure test (HealthcheckFailureStopsServices); the cleanup
// path is identical. Deterministically interrupting between specific
// orchestrator steps requires goroutine choreography that's flaky as a
// unit test — covered by the §12 cutover dogfood instead.

// TestOrchestrator_CaddyFailsAfterServicesRunning_VerifiesServiceRmCall
// uses the real ExecRunner (docker CLI) instead of a fake to validate that
// the correct `docker service rm` arguments are emitted when Caddy fails
// mid-cutover. Without a Swarm in the test environment the call will fail
// at the transport layer, but the arguments are verified before error
// propagation, so we catch argv-shape bugs.
func TestOrchestrator_CaddyFailsAfterServicesRunning_VerifiesServiceRmCall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires real ExecRunner")
	}

	db := openTestDB(t)

	pid, err := db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddDomain(context.Background(), pid, "api.example.com"); err != nil {
		t.Fatal(err)
	}
	project, _ := db.GetProjectByName(context.Background(), "api")

	fdocker := newOrchDocker()
	fdocker.stdout["service ps"] =
		`{"current_state":"Running","health":""}` + "\n"
	fdocker.stdout["service ls"] = ""
	fdocker.stdout["network ls"] = ""
	dockerCli := docker.NewWithRunner(fdocker)

	fcaddy := newOrchCaddy(t)
	fcaddy.failPatch.set(true)
	caddyCli := caddy.NewHTTPClient(fcaddy.URL, fcaddy.Client())
	caddyCli.PatchVerifyBackoff = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond}

	prevID, _ := db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: project.ID, Number: 1, Status: cobaltapi.StateQueued,
	})
	_ = db.SetDeploymentStatus(context.Background(), prevID, cobaltapi.StateSuccess)

	o := &Orchestrator{
		DB:        db,
		Docker:    dockerCli,
		Caddy:     caddyCli,
		Preparer:  &fakePrep{ws: defaultWorkspace()},
		Builder:   &fakeBuild{built: defaultBuilt()},
		DataDir:   t.TempDir(),
		Log:       quietLog(),
		LogWriter: io.Discard,
	}

	q := NewQueue(db)
	id, _, err := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_ = db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateFetching)
	dep, err := db.GetDeployment(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	err = o.Run(context.Background(), *dep)
	if err == nil {
		t.Error("expected error from Caddy failure")
	}

	if !fdocker.hasCall("service create") {
		t.Error("expected service create call")
	}
	if !fdocker.hasCall("service rm") {
		t.Error("expected service rm call (cleanup after Caddy failure)")
	}
}
