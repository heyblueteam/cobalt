package api

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Test plumbing: spin up an httptest server with the api handler bound
// against a fresh sqlite store + in-memory queue/dispatcher (nil
// dispatcher is OK; api.go tolerates it).

type testEnv struct {
	t       *testing.T
	srv     *httptest.Server
	db      *store.DB
	queue   *deploy.Queue
	client  *http.Client
	dataDir string
}

func newEnv(t *testing.T) *testEnv {
	return newEnvWithDataDir(t, "")
}

func newEnvWithDataDir(t *testing.T, dataDir string) *testEnv {
	t.Helper()
	db := openTestDB(t)
	q := deploy.NewQueue(db)
	mux := http.NewServeMux()
	h := &Handler{
		DB:      db,
		Queue:   q,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataDir: dataDir,
	}
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, srv: srv, db: db, queue: q, client: srv.Client(), dataDir: dataDir}
}

func (e *testEnv) do(method, path string, body any) *http.Response {
	e.t.Helper()
	var buf io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, buf)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("do: %v", err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func mustStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d want %d (body=%s)", resp.StatusCode, want, string(body))
	}
}

// --- projects ---

// TestDeleteProject_RemovesOnDiskArtifacts asserts that the project's
// repo dir and deploy log dir under DataDir are wiped on DELETE so a
// later project re-created with the same name doesn't inherit stale
// log entries or repo state.
func TestDeleteProject_RemovesOnDiskArtifacts(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	e := newEnvWithDataDir(t, tmp)

	resp := e.do(http.MethodPost, "/api/projects", cobaltapi.ProjectCreateRequest{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// Seed both dirs with sentinel files to prove they get removed.
	repoDir := filepath.Join(tmp, "projects", "api")
	logDir := filepath.Join(tmp, "logs", "deployments", "api")
	if err := os.MkdirAll(filepath.Join(repoDir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "1.log"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp = e.do(http.MethodDelete, "/api/projects/api", nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	if _, err := os.Stat(repoDir); !os.IsNotExist(err) {
		t.Errorf("repo dir still exists after delete: err=%v", err)
	}
	if _, err := os.Stat(logDir); !os.IsNotExist(err) {
		t.Errorf("log dir still exists after delete: err=%v", err)
	}
}

func TestProjectsCRUD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	// Create.
	resp := e.do(http.MethodPost, "/api/projects", cobaltapi.ProjectCreateRequest{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	mustStatus(t, resp, http.StatusCreated)
	got := decode[cobaltapi.Project](t, resp)
	if got.Name != "api" || got.ID == 0 {
		t.Errorf("create: %+v", got)
	}

	// Conflict on duplicate name.
	resp = e.do(http.MethodPost, "/api/projects", cobaltapi.ProjectCreateRequest{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	mustStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	// List.
	resp = e.do(http.MethodGet, "/api/projects", nil)
	mustStatus(t, resp, http.StatusOK)
	list := decode[[]cobaltapi.Project](t, resp)
	if len(list) != 1 {
		t.Errorf("list: %d, want 1", len(list))
	}

	// Get single.
	resp = e.do(http.MethodGet, "/api/projects/api", nil)
	mustStatus(t, resp, http.StatusOK)
	one := decode[cobaltapi.Project](t, resp)
	if one.GithubRepo != "h/api" {
		t.Errorf("get: %+v", one)
	}

	// Get missing → 404.
	resp = e.do(http.MethodGet, "/api/projects/nope", nil)
	mustStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()

	// Rename.
	resp = e.do(http.MethodPatch, "/api/projects/api", cobaltapi.ProjectRenameRequest{Name: "newapi"})
	mustStatus(t, resp, http.StatusOK)
	renamed := decode[cobaltapi.Project](t, resp)
	if renamed.Name != "newapi" || renamed.ID != got.ID {
		t.Errorf("rename: id changed or name wrong: %+v", renamed)
	}

	// Delete.
	resp = e.do(http.MethodDelete, "/api/projects/newapi", nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(http.MethodGet, "/api/projects/newapi", nil)
	mustStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestCreateProject_ValidationErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cases := []struct {
		name string
		body cobaltapi.ProjectCreateRequest
	}{
		{"empty name", cobaltapi.ProjectCreateRequest{Name: "", GithubRepo: "h/x", Branch: "main"}},
		{"name with slash", cobaltapi.ProjectCreateRequest{Name: "a/b", GithubRepo: "h/x", Branch: "main"}},
		{"bad repo", cobaltapi.ProjectCreateRequest{Name: "x", GithubRepo: "no-slash", Branch: "main"}},
		{"empty branch", cobaltapi.ProjectCreateRequest{Name: "x", GithubRepo: "h/x", Branch: ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := e.do(http.MethodPost, "/api/projects", c.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("got %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestCreateProject_WithDomainAttachesIt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := e.do(http.MethodPost, "/api/projects", cobaltapi.ProjectCreateRequest{
		Name: "web", GithubRepo: "h/web", Branch: "main", Domain: "web.example.com",
	})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/projects/web/domains", nil)
	mustStatus(t, resp, http.StatusOK)
	doms := decode[[]cobaltapi.Domain](t, resp)
	if len(doms) != 1 || doms[0].Name != "web.example.com" {
		t.Errorf("domains: %+v", doms)
	}
}

// --- env ---

func setupProject(t *testing.T, e *testEnv, name string) {
	t.Helper()
	resp := e.do(http.MethodPost, "/api/projects", cobaltapi.ProjectCreateRequest{
		Name: name, GithubRepo: "h/" + name, Branch: "main",
	})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// TestListEnv_StaleFlagOnUpdatedAfterDeploy seeds a successful
// deployment, then sets a new env var. The new var was written
// after the deploy started, so list must mark it stale; vars set
// before the deploy must not be flagged.
func TestListEnv_StaleFlagOnUpdatedAfterDeploy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	// Pre-deploy var: written before the deploy "started".
	resp := e.do(http.MethodPost, "/api/projects/api/env", cobaltapi.EnvSetRequest{
		Vars: map[string]string{"OLD_VAR": "1"},
	})
	mustStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Seed a successful deployment whose started_at is now.
	pid, err := e.db.GetProjectByName(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	depID, err := e.db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: pid.ID, Number: 1, Status: cobaltapi.StateQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	// SetDeploymentStatus to fetching stamps started_at; success
	// stamps finished_at. We need started_at set.
	_ = e.db.SetDeploymentStatus(context.Background(), depID, cobaltapi.StateFetching)
	_ = e.db.SetDeploymentStatus(context.Background(), depID, cobaltapi.StateSuccess)

	// Wait long enough for any post-deploy env writes to land at a
	// later unix-second than the deployment's started_at.
	time.Sleep(1100 * time.Millisecond)

	// Post-deploy var: should be flagged stale.
	resp = e.do(http.MethodPost, "/api/projects/api/env", cobaltapi.EnvSetRequest{
		Vars: map[string]string{"NEW_VAR": "2"},
	})
	mustStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/projects/api/env", nil)
	mustStatus(t, resp, http.StatusOK)
	got := decode[[]cobaltapi.EnvVar](t, resp)
	if len(got) != 2 {
		t.Fatalf("got %d vars, want 2", len(got))
	}
	for _, v := range got {
		switch v.Key {
		case "OLD_VAR":
			if v.Stale {
				t.Errorf("OLD_VAR should not be stale (set before deploy): %+v", v)
			}
		case "NEW_VAR":
			if !v.Stale {
				t.Errorf("NEW_VAR should be stale (set after deploy): %+v", v)
			}
		default:
			t.Errorf("unexpected key %q", v.Key)
		}
		if v.UpdatedAt == 0 {
			t.Errorf("%s: UpdatedAt is zero", v.Key)
		}
	}
}

// TestListEnv_NoStalenessUntilFirstDeploy asserts that a project
// with no successful deployment yet sees Stale=false on every var,
// even though their UpdatedAt is set.
func TestListEnv_NoStalenessUntilFirstDeploy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/env", cobaltapi.EnvSetRequest{
		Vars: map[string]string{"FOO": "1"},
	})
	mustStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/projects/api/env", nil)
	mustStatus(t, resp, http.StatusOK)
	got := decode[[]cobaltapi.EnvVar](t, resp)
	if len(got) != 1 || got[0].Stale {
		t.Errorf("pre-deploy stale flag set: %+v", got)
	}
}

func TestEnvCRUD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	// Set bulk.
	resp := e.do(http.MethodPost, "/api/projects/api/env", cobaltapi.EnvSetRequest{
		Vars: map[string]string{"FOO": "1", "BAR": "2"},
	})
	mustStatus(t, resp, http.StatusOK)
	got := decode[[]cobaltapi.EnvVar](t, resp)
	if len(got) != 2 {
		t.Errorf("set returned %d, want 2", len(got))
	}

	// List.
	resp = e.do(http.MethodGet, "/api/projects/api/env", nil)
	mustStatus(t, resp, http.StatusOK)
	got = decode[[]cobaltapi.EnvVar](t, resp)
	keys := map[string]string{}
	for _, v := range got {
		keys[v.Key] = v.Value
	}
	if keys["FOO"] != "1" || keys["BAR"] != "2" {
		t.Errorf("env: %v", keys)
	}

	// Update one + add another. Response should be the rows we just
	// upserted (not the project's whole env state) so a partial set
	// doesn't echo unrelated keys back to the operator.
	resp = e.do(http.MethodPost, "/api/projects/api/env", cobaltapi.EnvSetRequest{
		Vars: map[string]string{"FOO": "999", "BAZ": "3"},
	})
	mustStatus(t, resp, http.StatusOK)
	got = decode[[]cobaltapi.EnvVar](t, resp)
	if len(got) != 2 {
		t.Errorf("set response: got %d rows, want 2 (only the upserted keys)", len(got))
	}
	gotMap := map[string]string{}
	for _, v := range got {
		gotMap[v.Key] = v.Value
	}
	if gotMap["FOO"] != "999" || gotMap["BAZ"] != "3" {
		t.Errorf("set response keys: %v, want FOO=999 BAZ=3", gotMap)
	}
	if _, untouched := gotMap["BAR"]; untouched {
		t.Errorf("set response leaked untouched key BAR: %v", gotMap)
	}

	// Confirm the project's full env still includes the untouched BAR.
	resp = e.do(http.MethodGet, "/api/projects/api/env", nil)
	mustStatus(t, resp, http.StatusOK)
	got = decode[[]cobaltapi.EnvVar](t, resp)
	if len(got) != 3 {
		t.Errorf("after upsert: list len %d, want 3", len(got))
	}

	// Delete.
	resp = e.do(http.MethodDelete, "/api/projects/api/env/BAR", nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(http.MethodGet, "/api/projects/api/env", nil)
	got = decode[[]cobaltapi.EnvVar](t, resp)
	if len(got) != 2 {
		t.Errorf("after delete: %d, want 2", len(got))
	}

	// Delete missing.
	resp = e.do(http.MethodDelete, "/api/projects/api/env/DOES_NOT_EXIST", nil)
	mustStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestSetEnv_RedeployEnqueuesDeployment(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")
	resp := e.do(http.MethodPost, "/api/projects/api/env", cobaltapi.EnvSetRequest{
		Vars:     map[string]string{"FOO": "1"},
		Redeploy: true,
	})
	mustStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	deps, _ := e.db.QueuedDeployments(context.Background())
	if len(deps) != 1 {
		t.Errorf("redeploy didn't enqueue: queued=%d", len(deps))
	}
}

// --- domains ---

// TestDomainsAddRedirect_RequiresPrimaryTarget asserts the API rejects
// a redirect that points at a host the project doesn't yet own.
func TestDomainsAddRedirect_RequiresPrimaryTarget(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{
		Name:       "www.example.com",
		RedirectTo: "example.com",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestDomainsAddRedirect_OK installs a primary, then a redirect
// pointing at it, and verifies the list reflects both rows with their
// types correctly populated.
func TestDomainsAddRedirect_OK(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/domains",
		cobaltapi.DomainAddRequest{Name: "example.com"})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	resp = e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{
		Name:       "www.example.com",
		RedirectTo: "example.com",
	})
	mustStatus(t, resp, http.StatusCreated)
	got := decode[cobaltapi.Domain](t, resp)
	if got.Type != cobaltapi.DomainTypeRedirect || got.RedirectTo != "example.com" {
		t.Errorf("redirect response wrong: %+v", got)
	}

	resp = e.do(http.MethodGet, "/api/projects/api/domains", nil)
	list := decode[[]cobaltapi.Domain](t, resp)
	if len(list) != 2 {
		t.Fatalf("len: %d, want 2", len(list))
	}
	for _, d := range list {
		switch d.Name {
		case "example.com":
			if d.Type != cobaltapi.DomainTypePrimary {
				t.Errorf("apex should be primary: %+v", d)
			}
		case "www.example.com":
			if d.Type != cobaltapi.DomainTypeRedirect || d.RedirectTo != "example.com" {
				t.Errorf("www should redirect to apex: %+v", d)
			}
		}
	}
}

// TestDomainsRemovePrimary_CascadesRedirects asserts that deleting the
// primary also removes any redirect rows pointing at it (no dangling
// 301s in Caddy).
func TestDomainsRemovePrimary_CascadesRedirects(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/domains",
		cobaltapi.DomainAddRequest{Name: "example.com"})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
	resp = e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{
		Name: "www.example.com", RedirectTo: "example.com",
	})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	resp = e.do(http.MethodDelete, "/api/projects/api/domains/example.com", nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/projects/api/domains", nil)
	list := decode[[]cobaltapi.Domain](t, resp)
	if len(list) != 0 {
		t.Errorf("redirects didn't cascade with primary delete: %+v", list)
	}
}

// TestDomainsAddRedirect_RejectsSelfRedirect refuses requests where
// name == redirectTo.
func TestDomainsAddRedirect_RejectsSelfRedirect(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/domains",
		cobaltapi.DomainAddRequest{Name: "example.com"})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	resp = e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{
		Name: "example.com", RedirectTo: "example.com",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestDomainsCRUD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{Name: "api.example.com"})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
	resp = e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{Name: "alt.example.com"})
	mustStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/projects/api/domains", nil)
	doms := decode[[]cobaltapi.Domain](t, resp)
	if len(doms) != 2 {
		t.Errorf("list: %d, want 2", len(doms))
	}

	// Re-adding a domain that's already attached must 409 — silently
	// no-op'ing here would let the CLI claim "added" when nothing
	// changed.
	resp = e.do(http.MethodPost, "/api/projects/api/domains", cobaltapi.DomainAddRequest{Name: "alt.example.com"})
	mustStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	resp = e.do(http.MethodDelete, "/api/projects/api/domains/api.example.com", nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	resp = e.do(http.MethodDelete, "/api/projects/api/domains/api.example.com", nil)
	mustStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

// --- deployments ---

func TestDeploymentsCRUD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	// Create deploy.
	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{
		Commit: "abc123",
	})
	mustStatus(t, resp, http.StatusAccepted)
	dep := decode[cobaltapi.Deployment](t, resp)
	if dep.Number != 1 || dep.CommitSHA != "abc123" {
		t.Errorf("create deploy: %+v", dep)
	}
	if dep.Status != cobaltapi.StateQueued {
		t.Errorf("status: %q, want queued", dep.Status)
	}

	// List.
	resp = e.do(http.MethodGet, "/api/projects/api/deployments", nil)
	mustStatus(t, resp, http.StatusOK)
	list := decode[[]cobaltapi.Deployment](t, resp)
	if len(list) != 1 {
		t.Errorf("list: %d, want 1", len(list))
	}

	// Get by id.
	resp = e.do(http.MethodGet, "/api/deployments/"+itoa(dep.ID), nil)
	mustStatus(t, resp, http.StatusOK)
	one := decode[cobaltapi.Deployment](t, resp)
	if one.ID != dep.ID {
		t.Errorf("get: id mismatch")
	}

	// Cancel queued → terminal canceled.
	resp = e.do(http.MethodPost, "/api/deployments/"+itoa(dep.ID)+"/cancel", nil)
	mustStatus(t, resp, http.StatusOK)
	canceled := decode[cobaltapi.Deployment](t, resp)
	if canceled.Status != cobaltapi.StateCanceled {
		t.Errorf("cancel: status %q, want canceled", canceled.Status)
	}
}

func TestListDeployments_Limit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")
	for i := 0; i < 5; i++ {
		resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{})
		mustStatus(t, resp, http.StatusAccepted)
		resp.Body.Close()
	}
	resp := e.do(http.MethodGet, "/api/projects/api/deployments?limit=2", nil)
	mustStatus(t, resp, http.StatusOK)
	list := decode[[]cobaltapi.Deployment](t, resp)
	if len(list) != 2 {
		t.Errorf("limited: %d, want 2", len(list))
	}
	// Most-recent first.
	if list[0].Number != 5 || list[1].Number != 4 {
		t.Errorf("ordering: %+v", list)
	}
}

func TestCreateDeployment_NoBody(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")
	// No body — should still work; defaults applied.
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/projects/api/deployments", nil)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, resp, http.StatusAccepted)
	resp.Body.Close()
}

func TestErrorBodyShape(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := e.do(http.MethodGet, "/api/projects/nope", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"error"`) {
		t.Errorf("error body shape: %s", string(body))
	}
}

// --- force-cancel deployment tests ---

// gatedRunner blocks every Run call on a per-id gate so a test can hold
// a deploy in fetching/building deterministically. Mirrors the
// recordingRunner pattern from internal/server/deploy/dispatcher_test.go,
// duplicated here to avoid leaking deploy package internals into api.
type gatedRunner struct {
	mu    sync.Mutex
	calls map[int64]struct{}
	gate  chan struct{} // closed → release all runs
}

func newGatedRunner() *gatedRunner {
	return &gatedRunner{
		calls: map[int64]struct{}{},
		gate:  make(chan struct{}),
	}
}

func (r *gatedRunner) Run(ctx context.Context, d store.Deployment) error {
	r.mu.Lock()
	r.calls[d.ID] = struct{}{}
	r.mu.Unlock()
	select {
	case <-r.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *gatedRunner) sawCall(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.calls[id]
	return ok
}

// newEnvWithDispatcher returns a testEnv whose handler has a real
// dispatcher wired up against a gated runner. Tests that exercise the
// --force code path need this; the bare newEnv leaves Dispatcher nil.
func newEnvWithDispatcher(t *testing.T) (*testEnv, *gatedRunner, *deploy.Dispatcher) {
	t.Helper()
	db := openTestDB(t)
	q := deploy.NewQueue(db)
	runner := newGatedRunner()
	disp := deploy.NewDispatcher(db, runner,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		deploy.DispatcherOpts{PollInterval: 50 * time.Millisecond})
	disp.Start(context.Background())
	t.Cleanup(disp.Stop)

	mux := http.NewServeMux()
	h := &Handler{
		DB:         db,
		Queue:      q,
		Dispatcher: disp,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, srv: srv, db: db, queue: q, client: srv.Client()}, runner, disp
}

// waitForStatus polls a deployment row until it reaches `want` or the
// 2s deadline elapses. Used by the force tests to synchronize with
// dispatcher transitions.
func waitForStatus(t *testing.T, db *store.DB, id int64, want cobaltapi.State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep, err := db.GetDeployment(context.Background(), id)
		if err == nil && dep.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	dep, _ := db.GetDeployment(context.Background(), id)
	t.Fatalf("deploy %d: never reached %q (last=%q)", id, want, dep.Status)
}

// TestCreateDeployment_Force_CancelsFetchingDeploy enqueues a deploy,
// waits for it to enter fetching, then enqueues a second with
// --force. The first should end canceled; the second should run to
// success once the gate is released.
func TestCreateDeployment_Force_CancelsFetchingDeploy(t *testing.T) {
	t.Parallel()
	e, runner, _ := newEnvWithDispatcher(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{Commit: "old"})
	mustStatus(t, resp, http.StatusAccepted)
	first := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	waitForStatus(t, e.db, first.ID, cobaltapi.StateFetching)

	resp = e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{
		Commit: "new", Force: true,
	})
	mustStatus(t, resp, http.StatusAccepted)
	second := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	if second.CancelledInflightId != first.ID {
		t.Errorf("CancelledInflightId: got %d, want %d", second.CancelledInflightId, first.ID)
	}

	waitForStatus(t, e.db, first.ID, cobaltapi.StateCanceled)
	close(runner.gate)
	waitForStatus(t, e.db, second.ID, cobaltapi.StateSuccess)
}

// TestCreateDeployment_Force_RejectedDuringSwapping confirms the API
// returns 409 Conflict when --force is requested against a deploy
// already in cutover, and that the original deploy is not interrupted.
func TestCreateDeployment_Force_RejectedDuringSwapping(t *testing.T) {
	t.Parallel()
	e, runner, _ := newEnvWithDispatcher(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{Commit: "old"})
	mustStatus(t, resp, http.StatusAccepted)
	first := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	waitForStatus(t, e.db, first.ID, cobaltapi.StateFetching)

	// Manually push the row into swapping; the runner is still gated.
	if err := e.db.SetDeploymentStatus(context.Background(), first.ID, cobaltapi.StateSwapping); err != nil {
		t.Fatalf("set swapping: %v", err)
	}

	resp = e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{
		Commit: "new", Force: true,
	})
	mustStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	// First deploy should still complete naturally.
	close(runner.gate)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := e.db.GetDeployment(context.Background(), first.ID)
		if dep.Status == cobaltapi.StateCanceled {
			t.Fatalf("deploy was canceled despite swapping refusal: %+v", dep)
		}
		if dep.Status.IsTerminal() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("first deploy never reached terminal state")
}

// TestCreateDeployment_Force_OnIdleProjectJustEnqueues asserts that
// --force on a project with no in-flight deploy behaves like a normal
// enqueue: 202 Accepted, CancelledInflightId == 0.
func TestCreateDeployment_Force_OnIdleProjectJustEnqueues(t *testing.T) {
	t.Parallel()
	e, runner, _ := newEnvWithDispatcher(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{
		Commit: "first", Force: true,
	})
	mustStatus(t, resp, http.StatusAccepted)
	d := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	if d.CancelledInflightId != 0 {
		t.Errorf("CancelledInflightId on idle project: got %d, want 0", d.CancelledInflightId)
	}

	close(runner.gate)
	waitForStatus(t, e.db, d.ID, cobaltapi.StateSuccess)
	if !runner.sawCall(d.ID) {
		t.Errorf("runner never invoked for idle-project force deploy")
	}
}

// TestCreateDeployment_Force_BodyParses asserts that the JSON wire
// format threads Force through correctly. A no-op for parsing
// regressions, since other tests exercise the path end-to-end, but
// keeps a low-cost guard against a malformed tag rename.
func TestCreateDeployment_Force_BodyParses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	setupProject(t, e, "api")

	// Dispatcher is nil here; the force branch in CreateDeployment
	// short-circuits when the dispatcher is unset, so this just
	// confirms the field is accepted and the row is enqueued.
	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{
		Commit: "x", Force: true,
	})
	mustStatus(t, resp, http.StatusAccepted)
	d := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	if d.Status != cobaltapi.StateQueued {
		t.Errorf("status: %q, want queued", d.Status)
	}
}

// TestCancelDeployment_DuringSwapping_Returns409 confirms the safety
// improvement to the manual cancel path: cobalt deployments cancel <id>
// returns 409 Conflict when the target is in cutover, instead of
// silently logging.
func TestCancelDeployment_DuringSwapping_Returns409(t *testing.T) {
	t.Parallel()
	e, runner, _ := newEnvWithDispatcher(t)
	setupProject(t, e, "api")

	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{Commit: "x"})
	mustStatus(t, resp, http.StatusAccepted)
	d := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	waitForStatus(t, e.db, d.ID, cobaltapi.StateFetching)
	if err := e.db.SetDeploymentStatus(context.Background(), d.ID, cobaltapi.StateSwapping); err != nil {
		t.Fatalf("set swapping: %v", err)
	}

	resp = e.do(http.MethodPost, "/api/deployments/"+itoa(d.ID)+"/cancel", nil)
	mustStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	close(runner.gate)
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
