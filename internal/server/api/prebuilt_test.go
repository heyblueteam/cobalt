package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

var prebuiltRef = "ghcr.io/heyblueteam/blue-server@sha256:" + strings.Repeat("d", 64)

func setImageSource(t *testing.T, db *store.DB, id int64, source string) {
	t.Helper()
	p, err := db.GetProjectByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateProjectSource(context.Background(), id, p.GithubRepo, p.Branch, p.Path, p.WatchPaths, source); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDeployment_RegistryProjectRequiresImages(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, _ := e.db.CreateProject(context.Background(), store.Project{Name: "server", GithubRepo: "h/blue", Branch: "main", Path: "server"})
	setImageSource(t, e.db, id, validator.ImageSourceRegistry)

	resp := e.do(http.MethodPost, "/api/projects/server/deployments", cobaltapi.DeploymentCreateRequest{})
	defer resp.Body.Close()
	mustStatus(t, resp, http.StatusBadRequest)
	if deps, _ := e.db.QueuedDeployments(context.Background()); len(deps) != 0 {
		t.Fatalf("refused deploy was enqueued: %d", len(deps))
	}
}

func TestCreateDeployment_ImagesAreStoredAndReturned(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, _ := e.db.CreateProject(context.Background(), store.Project{Name: "server", GithubRepo: "h/blue", Branch: "main", Path: "server"})
	setImageSource(t, e.db, id, validator.ImageSourceRegistry)

	resp := e.do(http.MethodPost, "/api/projects/server/deployments", cobaltapi.DeploymentCreateRequest{
		Commit: "abc1234",
		Images: map[string]string{"default": prebuiltRef},
	})
	mustStatus(t, resp, http.StatusAccepted)
	got := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	if got.Images["default"] != prebuiltRef || got.CommitSHA != "abc1234" {
		t.Fatalf("response: %+v", got.Deployment)
	}
	deps, _ := e.db.QueuedDeployments(context.Background())
	if len(deps) != 1 || deps[0].ImageRefs["default"] != prebuiltRef {
		t.Fatalf("queued: %+v", deps)
	}
}

func TestCreateDeployment_RejectsUnpinnedImage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{Name: "api", GithubRepo: "h/blue", Branch: "main"})
	for _, ref := range []string{"ghcr.io/heyblueteam/blue-server:latest", "ghcr.io/x@sha256:abc"} {
		resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{
			Images: map[string]string{"default": ref},
		})
		mustStatus(t, resp, http.StatusBadRequest)
		resp.Body.Close()
	}
}

func TestCreateDeployment_BuildProjectUnchanged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{Name: "api", GithubRepo: "h/blue", Branch: "main"})
	resp := e.do(http.MethodPost, "/api/projects/api/deployments", cobaltapi.DeploymentCreateRequest{})
	mustStatus(t, resp, http.StatusAccepted)
	got := decode[cobaltapi.DeploymentCreateResponse](t, resp)
	if len(got.Images) != 0 {
		t.Fatalf("build deploy has images: %+v", got.Images)
	}
}

func TestUpdateProjectSource_ImageSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{Name: "server", GithubRepo: "h/blue", Branch: "main", Path: "server", WatchPaths: ""})

	registry := validator.ImageSourceRegistry
	resp := e.do(http.MethodPatch, "/api/projects/server/source", cobaltapi.ProjectUpdateSourceRequest{
		GithubRepo: "h/blue", Branch: "main", Path: "server", ImageSource: &registry,
	})
	mustStatus(t, resp, http.StatusOK)
	if got := decode[cobaltapi.Project](t, resp); got.ImageSource != registry {
		t.Fatalf("image source: %q", got.ImageSource)
	}

	// Omitting the field keeps the value (older CLIs).
	resp = e.do(http.MethodPatch, "/api/projects/server/source", cobaltapi.ProjectUpdateSourceRequest{
		GithubRepo: "h/blue", Branch: "main", Path: "server",
	})
	mustStatus(t, resp, http.StatusOK)
	if got := decode[cobaltapi.Project](t, resp); got.ImageSource != registry {
		t.Fatalf("omitted field reset image source to %q", got.ImageSource)
	}

	bad := "ci"
	resp = e.do(http.MethodPatch, "/api/projects/server/source", cobaltapi.ProjectUpdateSourceRequest{
		GithubRepo: "h/blue", Branch: "main", Path: "server", ImageSource: &bad,
	})
	mustStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

func TestRegistriesAPI_NeverReturnsPassword(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := e.do(http.MethodPost, "/api/registries/ghcr.io", cobaltapi.RegistryCredentialSetRequest{Username: "bot", Password: "s3cret-token"})
	mustStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = e.do(http.MethodGet, "/api/registries", nil)
	mustStatus(t, resp, http.StatusOK)
	raw := readBodyString(t, resp)
	if strings.Contains(raw, "s3cret-token") || !strings.Contains(raw, `"registry":"ghcr.io"`) {
		t.Fatalf("list body: %s", raw)
	}

	resp = e.do(http.MethodPost, "/api/registries/ghcr.io", cobaltapi.RegistryCredentialSetRequest{Username: "bot"})
	mustStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	resp = e.do(http.MethodDelete, "/api/registries/ghcr.io", nil)
	mustStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(http.MethodDelete, "/api/registries/ghcr.io", nil)
	mustStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestWebhook_PushSkipsRegistryProject(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	ctx := context.Background()
	serverID, _ := e.db.CreateProject(ctx, store.Project{Name: "server", GithubRepo: "acme/monorepo", Branch: "main", Path: "server"})
	setImageSource(t, e.db, serverID, validator.ImageSourceRegistry)
	_, _ = e.db.CreateProject(ctx, store.Project{Name: "api", GithubRepo: "acme/monorepo", Branch: "main", Path: "api"})

	payload := map[string]any{
		"ref":        "refs/heads/main",
		"after":      "abc123",
		"repository": map[string]any{"id": 1, "full_name": "acme/monorepo"},
		"commits":    []map[string]any{{"modified": []string{"server/src/main.rs", "api/src/index.ts"}}},
	}
	resp := e.post(t, "push", "delivery-1", payload)
	resp.Body.Close()
	deps, _ := e.db.QueuedDeployments(ctx)
	if len(deps) != 1 {
		t.Fatalf("queued: %d, want only the build project", len(deps))
	}
	if p, _ := e.db.GetProjectByID(ctx, deps[0].ProjectID); p.Name != "api" {
		t.Fatalf("queued project %q, want api", p.Name)
	}
}
