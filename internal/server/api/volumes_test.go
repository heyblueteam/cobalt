package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// volumesFakeDocker is a docker.Runner that records every shell-out
// and stages canned stdout per argv prefix. Lets us assert what the
// volume handlers tell docker to do without a real daemon.
type volumesFakeDocker struct {
	mu     sync.Mutex
	calls  []string
	stdout map[string]string
	// onRun, when set, is consulted for any `docker run --rm ...`
	// invocation; it gets stdin/stdout and can simulate import behavior.
	onRun func(stdin io.Reader, stdout io.Writer) error
}

func (f *volumesFakeDocker) Run(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	joined := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, joined)
	cb := f.onRun
	canned := f.stdout[firstWords(joined, 2)]
	f.mu.Unlock()

	if len(args) > 0 && args[0] == "run" && cb != nil {
		return cb(stdin, stdout)
	}
	if canned != "" && stdout != nil {
		_, _ = io.WriteString(stdout, canned)
	}
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	return nil
}

func firstWords(s string, n int) string {
	parts := strings.SplitN(s, " ", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, " ")
}

func newVolumesEnv(t *testing.T) (*testEnv, *volumesFakeDocker) {
	t.Helper()
	db := openTestDB(t)

	fdocker := &volumesFakeDocker{stdout: map[string]string{}}
	dockerCli := docker.NewWithRunner(fdocker)
	q := deploy.NewQueue(db)
	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:     db,
		Docker: dockerCli,
		Queue:  q,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{t: t, srv: srv, db: db, queue: q, client: srv.Client()}, fdocker
}

func TestVolumes_List(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})

	// Stub the `docker volume ls --filter label=cobalt.project.id=PID` call.
	fdocker.stdout["volume ls"] = docker.VolumeName(pid, "data") + "\n" + docker.VolumeName(pid, "uploads") + "\n"

	resp := e.do(http.MethodGet, "/api/projects/api/volumes", nil)
	mustStatus(t, resp, http.StatusOK)
	got := decode[[]cobaltapi.Volume](t, resp)
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	wantShort := map[string]string{
		"data":    docker.VolumeName(pid, "data"),
		"uploads": docker.VolumeName(pid, "uploads"),
	}
	for _, v := range got {
		if wantShort[v.Name] != v.FullName {
			t.Errorf("volume %+v not in expected map", v)
		}
	}
}

func TestVolumes_Export_StreamsTar(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})

	// Stub the existence-probe so the handler doesn't 404 the request.
	fdocker.stdout["volume ls"] = docker.VolumeName(pid, "data") + "\n"

	const tarBytes = "<<this-is-fake-tar-data>>"
	fdocker.onRun = func(_ io.Reader, stdout io.Writer) error {
		_, _ = io.WriteString(stdout, tarBytes)
		return nil
	}

	resp := e.do(http.MethodPost, "/api/projects/api/volumes/data/export", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-tar" {
		t.Errorf("Content-Type: %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "api-data.tar") {
		t.Errorf("Content-Disposition: %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fake-tar-data") {
		t.Errorf("body: %q", string(body))
	}
}

func TestVolumes_Import_ReadsBody(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})

	// Stub the existence-probe.
	fdocker.stdout["volume ls"] = docker.VolumeName(pid, "data") + "\n"

	var received []byte
	fdocker.onRun = func(stdin io.Reader, _ io.Writer) error {
		received, _ = io.ReadAll(stdin)
		return nil
	}

	body := bytes.NewBufferString("<<incoming-tar-payload>>")
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/projects/api/volumes/data/import", body)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(received), "incoming-tar-payload") {
		t.Errorf("docker stdin didn't see payload: %q", string(received))
	}
}

func TestVolumes_Export_404OnUnknownVolume(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})

	// `volume ls` returns nothing → handler must refuse rather than
	// shell out to docker (which would auto-create the volume and
	// return an empty tar).
	fdocker.stdout["volume ls"] = ""

	tarRan := false
	fdocker.onRun = func(_ io.Reader, _ io.Writer) error {
		tarRan = true
		return nil
	}

	resp := e.do(http.MethodPost, "/api/projects/api/volumes/typo/export", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
	if tarRan {
		t.Error("docker tar ran for a nonexistent volume; auto-create trap reopened")
	}
}

func TestVolumes_Import_404OnUnknownVolume(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})

	fdocker.stdout["volume ls"] = ""

	tarRan := false
	fdocker.onRun = func(_ io.Reader, _ io.Writer) error {
		tarRan = true
		return nil
	}

	body := bytes.NewBufferString("<<payload>>")
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/projects/api/volumes/typo/import", body)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
	if tarRan {
		t.Error("docker tar ran for a nonexistent volume on import")
	}
}

func TestVolumes_ProjectMissing(t *testing.T) {
	t.Parallel()
	e, _ := newVolumesEnv(t)
	resp := e.do(http.MethodGet, "/api/projects/nope/volumes", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: %d", resp.StatusCode)
	}
}
