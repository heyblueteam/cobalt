package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Test plumbing: spin up an httptest server with the api handler bound
// against a fresh sqlite store + in-memory queue/dispatcher (nil
// dispatcher is OK; api.go tolerates it).

type testEnv struct {
	t      *testing.T
	srv    *httptest.Server
	db     *store.DB
	queue  *deploy.Queue
	client *http.Client
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	db := openTestDB(t)
	q := deploy.NewQueue(db)
	mux := http.NewServeMux()
	h := &Handler{
		DB:    db,
		Queue: q,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, srv: srv, db: db, queue: q, client: srv.Client()}
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
