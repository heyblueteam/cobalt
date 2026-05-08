package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// helper that stubs the same `image ls` answer the rollback handler
// runs under volumesFakeDocker; reuses the volumes-test runner so
// one fake covers both shapes.
func seedSuccessfulDeployment(t *testing.T, e *testEnv, projectID int64, number int, cobaltfile string) int64 {
	t.Helper()
	id, err := e.db.CreateDeployment(context.Background(), store.Deployment{
		ProjectID: projectID, Number: number, Status: cobaltapi.StateQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cobaltfile != "" {
		if err := e.db.SetResolvedCobaltfile(context.Background(), id, cobaltfile); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateSuccess); err != nil {
		t.Fatal(err)
	}
	return id
}

const fixtureCobaltfile = `{"version":"1.0","services":{"web":{"type":"container","image":"default","port":3000}}}`

func TestRollback_404WhenNoPriorSuccess(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	// Only one successful deploy → no PRIOR success exists, default
	// form must 404.
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	_ = seedSuccessfulDeployment(t, e, pid, 1, fixtureCobaltfile)
	fdocker.stdout["image ls"] = "cobalt/project-api-default:1\n"

	resp := e.do(http.MethodPost, "/api/projects/api/rollback", cobaltapi.RollbackRequest{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestRollback_409WhenTargetIsCurrent(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	_ = seedSuccessfulDeployment(t, e, pid, 1, fixtureCobaltfile)
	fdocker.stdout["image ls"] = "cobalt/project-api-default:1\n"

	resp := e.do(http.MethodPost, "/api/projects/api/rollback",
		cobaltapi.RollbackRequest{To: 1})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status: got %d, want 409", resp.StatusCode)
	}
}

func TestRollback_410WhenImageMissing(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	_ = seedSuccessfulDeployment(t, e, pid, 1, fixtureCobaltfile)
	_ = seedSuccessfulDeployment(t, e, pid, 2, fixtureCobaltfile)
	// `image ls cobalt/project-api-default:1` returns nothing →
	// pruned, rollback must refuse.
	fdocker.stdout["image ls"] = ""

	resp := e.do(http.MethodPost, "/api/projects/api/rollback",
		cobaltapi.RollbackRequest{To: 1})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status: got %d, want 410", resp.StatusCode)
	}
	body := readBodyString(t, resp)
	if !strings.Contains(body, "no longer cached") {
		t.Errorf("body %q does not mention image being uncached", body)
	}
}

func TestRollback_404OnUnknownTargetNumber(t *testing.T) {
	t.Parallel()
	e, fdocker := newVolumesEnv(t)
	pid, _ := e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "h/api", Branch: "main",
	})
	_ = seedSuccessfulDeployment(t, e, pid, 1, fixtureCobaltfile)
	fdocker.stdout["image ls"] = "cobalt/project-api-default:1\n"

	resp := e.do(http.MethodPost, "/api/projects/api/rollback",
		cobaltapi.RollbackRequest{To: 99})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func readBodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
