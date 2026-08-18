package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func generateRSAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

// fakeGithub stands in for api.github.com during prune tests. It
// answers the three endpoints prune touches: app probe, token mint,
// and the installation repo list.
func fakeGithub(t *testing.T, repos []github.Repository) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 12345})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "ghs_testtoken", "expires_at": "2030-01-01T00:00:00Z",
		})
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": len(repos), "repositories": repos,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pruneEnv builds a handler wired to a fake GitHub and a real store,
// seeded with one app + one installation.
type pruneEnv struct {
	t      *testing.T
	srv    *httptest.Server
	db     *store.DB
	appID  int64 // local PK
	instID int64 // local PK
}

func newPruneEnv(t *testing.T, githubRepos []github.Repository) *pruneEnv {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()

	gh := fakeGithub(t, githubRepos)

	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:     db,
		Queue:  deploy.NewQueue(db),
		GitHub: github.NewClientWithBaseURL(gh.URL, nil),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	appID, err := db.CreateGithubApp(ctx, store.GithubApp{
		AppID:         12345,
		Owner:         "acme",
		PrivateKey:    generateRSAPEM(t),
		WebhookSecret: "s",
	})
	if err != nil {
		t.Fatalf("CreateGithubApp: %v", err)
	}
	instID, err := db.CreateGithubAppInstallation(ctx, store.GithubAppInstallation{
		AppID: appID, InstallationID: 777, AccountLogin: "acme",
	})
	if err != nil {
		t.Fatalf("CreateGithubAppInstallation: %v", err)
	}
	return &pruneEnv{t: t, srv: srv, db: db, appID: appID, instID: instID}
}

func (e *pruneEnv) prune() cobaltapi.PruneResponse {
	e.t.Helper()
	resp, err := e.srv.Client().Post(e.srv.URL+"/api/github-apps/prune", "application/json", nil)
	if err != nil {
		e.t.Fatalf("prune request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		e.t.Fatalf("prune status %d: %s", resp.StatusCode, body)
	}
	var out cobaltapi.PruneResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		e.t.Fatalf("decode: %v", err)
	}
	return out
}

// TestPrune_RefreshesRenamedRepoAndRetargetsProjects: GitHub reports
// the same repo ID under a new full_name. Prune must update the local
// row in place (not skip it as "already known") and retarget projects
// tracking the old name. This is the remediation path for renames whose
// webhook was missed — before this behavior existed, prune reported
// "0 changes" while token resolution stayed broken.
func TestPrune_RefreshesRenamedRepoAndRetargetsProjects(t *testing.T) {
	t.Parallel()
	e := newPruneEnv(t, []github.Repository{
		{ID: 4242, FullName: "acme/newname", Private: true, DefaultBranch: "main"},
	})
	ctx := context.Background()

	_, _ = e.db.AddGithubAppRepo(ctx, store.GithubAppRepo{
		InstallationID: e.instID, RepoID: 4242, FullName: "acme/oldname", Private: true, DefaultBranch: "main",
	})
	_, _ = e.db.CreateProject(ctx, store.Project{
		Name: "api", GithubRepo: "acme/oldname", Branch: "main",
	})

	resp := e.prune()
	if resp.ReposUpdated != 1 {
		t.Errorf("ReposUpdated: %d, want 1", resp.ReposUpdated)
	}
	if resp.ProjectsRetargeted != 1 {
		t.Errorf("ProjectsRetargeted: %d, want 1", resp.ProjectsRetargeted)
	}
	if resp.ReposAdded != 0 || resp.ReposRemoved != 0 {
		t.Errorf("added/removed: %d/%d, want 0/0", resp.ReposAdded, resp.ReposRemoved)
	}

	repo, err := e.db.GetGithubRepoByRepoID(ctx, 4242)
	if err != nil {
		t.Fatalf("GetGithubRepoByRepoID: %v", err)
	}
	if repo.FullName != "acme/newname" {
		t.Errorf("repo full_name: %q, want acme/newname", repo.FullName)
	}
	p, err := e.db.GetProjectByName(ctx, "api")
	if err != nil {
		t.Fatalf("GetProjectByName: %v", err)
	}
	if p.GithubRepo != "acme/newname" {
		t.Errorf("project github_repo: %q, want acme/newname", p.GithubRepo)
	}
}

// TestPrune_UnchangedRepoIsUntouched: identical metadata on both sides
// must produce a zero-change response (no gratuitous writes).
func TestPrune_UnchangedRepoIsUntouched(t *testing.T) {
	t.Parallel()
	e := newPruneEnv(t, []github.Repository{
		{ID: 1, FullName: "acme/api", Private: true, DefaultBranch: "main"},
	})
	ctx := context.Background()
	_, _ = e.db.AddGithubAppRepo(ctx, store.GithubAppRepo{
		InstallationID: e.instID, RepoID: 1, FullName: "acme/api", Private: true, DefaultBranch: "main",
	})

	resp := e.prune()
	if resp.ReposUpdated != 0 || resp.ReposAdded != 0 || resp.ReposRemoved != 0 || resp.ProjectsRetargeted != 0 {
		t.Errorf("want all-zero response, got %+v", resp)
	}
}

// TestPrune_StillAddsAndRemoves: the pre-existing add/remove behavior
// must survive the in-place-update change.
func TestPrune_StillAddsAndRemoves(t *testing.T) {
	t.Parallel()
	e := newPruneEnv(t, []github.Repository{
		{ID: 2, FullName: "acme/added", Private: false, DefaultBranch: "main"},
	})
	ctx := context.Background()
	_, _ = e.db.AddGithubAppRepo(ctx, store.GithubAppRepo{
		InstallationID: e.instID, RepoID: 3, FullName: "acme/removed", Private: false,
	})

	resp := e.prune()
	if resp.ReposAdded != 1 {
		t.Errorf("ReposAdded: %d, want 1", resp.ReposAdded)
	}
	if resp.ReposRemoved != 1 {
		t.Errorf("ReposRemoved: %d, want 1", resp.ReposRemoved)
	}
	if _, err := e.db.GetGithubRepoByRepoID(ctx, 2); err != nil {
		t.Errorf("added repo missing: %v", err)
	}
	if _, err := e.db.GetGithubRepoByRepoID(ctx, 3); err == nil {
		t.Error("removed repo still present")
	}
}
