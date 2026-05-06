package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

type mockAPI struct {
	srv         *httptest.Server
	handler     func(w http.ResponseWriter, r *http.Request)
	lastMethod  string
	lastPath    string
	lastQuery   string
	lastBody    []byte
	callCount   int
}

func newMockAPI() *mockAPI {
	m := &mockAPI{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.lastMethod = r.Method
		m.lastPath = r.URL.Path
		m.lastQuery = r.URL.RawQuery
		if r.Body != nil {
			m.lastBody, _ = jsonReadAll(r.Body)
		}
		m.callCount++
		if m.handler != nil {
			m.handler(w, r)
		}
	}))
	return m
}

func (m *mockAPI) close() { m.srv.Close() }

func jsonReadAll(r io.Reader) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

func (m *mockAPI) respond(v any) {
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(v)
		w.Write(b)
	}
}

func (m *mockAPI) respondStatus(code int, body string) {
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(body))
	}
}

func (m *mockAPI) configPath(t *testing.T) error {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	cobaltDir := filepath.Join(tmpDir, ".cobalt")
	if err := os.MkdirAll(cobaltDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cobaltDir, "config.json")
	return cliconfig.Save(configPath, &cliconfig.Config{
		Servers:       map[string]cliconfig.Server{"test": {Host: m.srv.URL, APIKey: "k"}},
		DefaultServer: "test",
	})
}

func TestProjectsList(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Project{
		{ID: 1, Name: "api", GithubRepo: "heyblueteam/api", Branch: "main"},
		{ID: 2, Name: "web", GithubRepo: "heyblueteam/web", Branch: "main"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"projects", "list"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "api") || !contains(got, "web") {
		t.Errorf("expected project names: %s", got)
	}
}

func TestProjectsListJSON(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond([]cobaltapi.Project{
		{ID: 1, Name: "api", GithubRepo: "heyblueteam/api", Branch: "main"},
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"projects", "list", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected JSON output")
	}
}

func TestProjectsAdd(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Project{
		ID: 1, Name: "api", GithubRepo: "heyblueteam/api", Branch: "main",
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := output.Stdout
	output.Stdout = &buf
	defer func() { output.Stdout = oldStdout }()

	root := newRootCmd()
	root.SetArgs([]string{"projects", "add", "api", "--github", "heyblueteam/api", "--branch", "main"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !contains(got, "created") {
		t.Errorf("expected 'created': %s", got)
	}
	if api.lastPath != "/api/projects" {
		t.Errorf("path: got %q, want /api/projects", api.lastPath)
	}
}

func TestProjectsAddMissingGitHub(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"projects", "add", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for missing --github")
	}
	if !contains(err.Error(), "github") {
		t.Errorf("expected github error, got: %v", err)
	}
}

func TestProjectsAddWithDomain(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Project{
		ID: 1, Name: "api", GithubRepo: "heyblueteam/api", Branch: "main",
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"projects", "add", "api", "--github", "heyblueteam/api", "--domain", "api.blue.cc"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestProjectsRemove(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(nil)

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"projects", "remove", "api", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if api.lastMethod != "DELETE" {
		t.Errorf("method: got %q, want DELETE", api.lastMethod)
	}
}

func TestProjectsRename(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	api.respond(&cobaltapi.Project{
		ID: 1, Name: "api-v2", GithubRepo: "heyblueteam/api", Branch: "main",
	})

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"projects", "rename", "api", "api-v2", "--yes"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if api.lastMethod != "PATCH" {
		t.Errorf("method: got %q, want PATCH", api.lastMethod)
	}
	if !contains(string(api.lastBody), "api-v2") {
		t.Errorf("body: got %q, want api-v2", string(api.lastBody))
	}
}
