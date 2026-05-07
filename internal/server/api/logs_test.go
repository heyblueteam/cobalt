package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// streamEnv stands up a Handler whose DataDir points at a tempdir we
// can populate with synthetic deploy log files.
type streamEnv struct {
	t       *testing.T
	srv     *httptest.Server
	db      *store.DB
	dataDir string
}

func newStreamEnv(t *testing.T) *streamEnv {
	t.Helper()
	dataDir := t.TempDir()
	db := openTestDB(t)

	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:      db,
		Queue:   deploy.NewQueue(db),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataDir: dataDir,
	})
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &streamEnv{t: t, srv: srv, db: db, dataDir: dataDir}
}

func (e *streamEnv) seedDeploy(content string, status cobaltapi.State) (projectID, depID int64, depNum int) {
	e.t.Helper()
	pid, err := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	depNum = 1
	id, err := e.db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: pid, Number: depNum, Status: cobaltapi.StateQueued,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if status != cobaltapi.StateQueued {
		_ = e.db.SetDeploymentStatus(context.Background(), id, status)
	}
	if content != "" {
		path := deploy.DeployLogPath(e.dataDir, "api", depNum)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	return pid, id, depNum
}

// readSSE consumes the entire SSE response body and returns just the
// concatenated `data:` payloads (stripping prefixes and heartbeats).
func readSSE(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "data: ") {
			out.WriteString(strings.TrimPrefix(line, "data: "))
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func TestDeploymentOutput_TerminalDeployStreamsAndCloses(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	const content = "build started\nbuild finished\n"
	_, depID, _ := e.seedDeploy(content, cobaltapi.StateSuccess)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		e.srv.URL+"/api/deployments/"+itoa(depID)+"/output", nil)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: %d", resp.StatusCode)
	}
	got := readSSE(t, resp)
	if !strings.Contains(got, "build started") || !strings.Contains(got, "build finished") {
		t.Errorf("output missing expected lines: %q", got)
	}
}

func TestDeploymentOutput_OffsetSkipsBytes(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	const content = "lineA\nlineB\nlineC\n"
	_, depID, _ := e.seedDeploy(content, cobaltapi.StateSuccess)

	// Skip the first line (6 bytes including newline).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		e.srv.URL+"/api/deployments/"+itoa(depID)+"/output?offset=6", nil)
	resp, _ := e.srv.Client().Do(req)
	got := readSSE(t, resp)
	if strings.Contains(got, "lineA") {
		t.Errorf("offset didn't skip lineA: %q", got)
	}
	if !strings.Contains(got, "lineB") || !strings.Contains(got, "lineC") {
		t.Errorf("offset dropped lineB/C: %q", got)
	}
}

func TestDeploymentOutput_MissingDeployment(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	resp, err := http.Get(e.srv.URL + "/api/deployments/9999/output")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: %d, want 404", resp.StatusCode)
	}
}

func TestDeploymentOutput_BadOffset(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	_, depID, _ := e.seedDeploy("x", cobaltapi.StateSuccess)
	url := e.srv.URL + "/api/deployments/" + itoa(depID) + "/output?offset=-5"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status: %d, want 400 (url=%s, body=%s)", resp.StatusCode, url, string(body))
	}
}

func TestDeploymentOutput_FollowsInFlightDeploy(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	// Start with the deploy in progress + initial bytes.
	_, depID, _ := e.seedDeploy("first\n", cobaltapi.StateBuilding)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		e.srv.URL+"/api/deployments/"+itoa(depID)+"/output", nil)

	// Append more bytes shortly, then mark terminal so the handler closes.
	go func() {
		time.Sleep(150 * time.Millisecond)
		path := deploy.DeployLogPath(e.dataDir, "api", 1)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = f.WriteString("second\n")
		_ = f.Close()
		time.Sleep(150 * time.Millisecond)
		_ = e.db.SetDeploymentStatus(context.Background(), depID, cobaltapi.StateSuccess)
	}()

	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := readSSE(t, resp)
	if !strings.Contains(got, "first") {
		t.Errorf("missing first: %q", got)
	}
	if !strings.Contains(got, "second") {
		t.Errorf("follow didn't pick up appended bytes: %q", got)
	}
}

func TestDeploymentOutput_NoLogFileTreatedAsEmpty(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	_, depID, _ := e.seedDeploy("", cobaltapi.StateSuccess) // no content → no file written

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		e.srv.URL+"/api/deployments/"+itoa(depID)+"/output", nil)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestProjectLogs_NotDeployedYetReturns404(t *testing.T) {
	t.Parallel()
	e := newStreamEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "fresh", GithubRepo: "h/fresh", Branch: "main",
	})
	resp, err := http.Get(e.srv.URL + "/api/projects/fresh/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: %d, want 404", resp.StatusCode)
	}
}
