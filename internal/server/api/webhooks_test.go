package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// webhookEnv builds a Handler with a real sqlite store, registers
// public routes onto a mux, and returns the server + helpers.
type webhookEnv struct {
	t   *testing.T
	srv *httptest.Server
	db  *store.DB
	app *store.GithubApp
	q   *deploy.Queue
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()
	db := openTestDB(t)

	q := deploy.NewQueue(db)
	mux := http.NewServeMux()
	h := NewHandler(HandlerOpts{
		DB:    db,
		Queue: q,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.RegisterPublic(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Insert a GitHub App into the store so webhook auth can find it.
	appID, err := db.CreateGithubApp(context.Background(), store.GithubApp{
		AppID:         12345,
		Owner:         "acme",
		PrivateKey:    "irrelevant", // not used for webhook auth
		WebhookSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("CreateGithubApp: %v", err)
	}
	app, err := db.GetGithubApp(context.Background(), appID)
	if err != nil {
		t.Fatalf("GetGithubApp(%d): %v", appID, err)
	}
	if app == nil {
		t.Fatalf("GetGithubApp(%d): nil app, no error — store inconsistency", appID)
	}
	return &webhookEnv{t: t, srv: srv, db: db, app: app, q: q}
}

func (e *webhookEnv) post(t *testing.T, event, delivery string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(github.HeaderEvent, event)
	req.Header.Set(github.HeaderDelivery, delivery)
	req.Header.Set("X-GitHub-Hook-Installation-Target-ID", strconv.FormatInt(e.app.AppID, 10))
	mac := hmac.New(sha256.New, []byte(e.app.WebhookSecret))
	mac.Write(body)
	req.Header.Set(github.HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// --- tests ---

func TestWebhook_RejectsMissingAppID(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/webhooks/github", bytes.NewReader([]byte(`{}`)))
	resp, _ := e.srv.Client().Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", resp.StatusCode)
	}
}

func TestWebhook_RejectsUnknownApp(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/webhooks/github", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-GitHub-Hook-Installation-Target-ID", "99999")
	resp, _ := e.srv.Client().Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: %d, want 401", resp.StatusCode)
	}
}

func TestWebhook_RejectsBadSignature(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/webhooks/github", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-GitHub-Hook-Installation-Target-ID", strconv.FormatInt(e.app.AppID, 10))
	req.Header.Set(github.HeaderEvent, "push")
	req.Header.Set(github.HeaderSignature, "sha256=ffff")
	resp, _ := e.srv.Client().Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: %d, want 401", resp.StatusCode)
	}
}

func TestWebhook_PushEnqueuesDeployForTrackingProject(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	ctx := context.Background()

	// Create a project tracking acme/api on main.
	pid, err := e.db.CreateProject(ctx, store.Project{
		Name: "api", GithubRepo: "acme/api", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = pid

	// Send a push event for that branch.
	payload := map[string]any{
		"ref":     "refs/heads/main",
		"after":   "abc123",
		"deleted": false,
		"repository": map[string]any{
			"id":        1,
			"full_name": "acme/api",
		},
	}
	resp := e.post(t, "push", "delivery-1", payload)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status: %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()

	deps, _ := e.db.QueuedDeployments(ctx)
	if len(deps) != 1 {
		t.Errorf("queued: %d, want 1", len(deps))
	}
	if len(deps) > 0 && (deps[0].CommitSHA == nil || *deps[0].CommitSHA != "abc123") {
		t.Errorf("commit: %+v", deps[0].CommitSHA)
	}
}

func TestWebhook_PushOnUntrackedBranchIsNoOp(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "acme/api", Branch: "main",
	})

	payload := map[string]any{
		"ref":   "refs/heads/feature",
		"after": "abc123",
		"repository": map[string]any{
			"id": 1, "full_name": "acme/api",
		},
	}
	resp := e.post(t, "push", "delivery-1", payload)
	resp.Body.Close()
	deps, _ := e.db.QueuedDeployments(context.Background())
	if len(deps) != 0 {
		t.Errorf("queued: %d, want 0 (untracked branch)", len(deps))
	}
}

func TestWebhook_BranchDeleteIsNoOp(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "acme/api", Branch: "main",
	})
	payload := map[string]any{
		"ref":     "refs/heads/main",
		"after":   "0000000000000000000000000000000000000000",
		"deleted": true,
		"repository": map[string]any{
			"id": 1, "full_name": "acme/api",
		},
	}
	resp := e.post(t, "push", "delivery-1", payload)
	resp.Body.Close()
	deps, _ := e.db.QueuedDeployments(context.Background())
	if len(deps) != 0 {
		t.Errorf("queued: %d, want 0 (branch delete)", len(deps))
	}
}

func TestWebhook_DedupSkipsRepeatDelivery(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	_, _ = e.db.CreateProject(context.Background(), store.Project{
		Name: "api", GithubRepo: "acme/api", Branch: "main",
	})
	payload := map[string]any{
		"ref":   "refs/heads/main",
		"after": "abc123",
		"repository": map[string]any{
			"id": 1, "full_name": "acme/api",
		},
	}
	resp := e.post(t, "push", "delivery-1", payload)
	resp.Body.Close()
	resp = e.post(t, "push", "delivery-1", payload)
	resp.Body.Close()
	deps, _ := e.db.QueuedDeployments(context.Background())
	if len(deps) != 1 {
		t.Errorf("queued: %d, want 1 (dedup should suppress second)", len(deps))
	}
}

func TestWebhook_InstallationCreatedAddsRowAndRepos(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)

	payload := map[string]any{
		"action": "created",
		"installation": map[string]any{
			"id": 555, "app_id": e.app.AppID,
			"account": map[string]any{"login": "acme", "id": 7},
		},
		"repositories": []map[string]any{
			{"id": 1, "full_name": "acme/api", "private": true},
			{"id": 2, "full_name": "acme/web", "private": false},
		},
	}
	resp := e.post(t, "installation", "delivery-2", payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status: %d", resp.StatusCode)
	}

	inst, err := e.db.GetGithubAppInstallationByInstallationID(context.Background(), 555)
	if err != nil {
		t.Fatalf("installation lookup: %v", err)
	}
	repos, _ := e.db.ListGithubReposForInstallation(context.Background(), inst.ID)
	if len(repos) != 2 {
		t.Errorf("repos: %d, want 2", len(repos))
	}
}

func TestWebhook_InstallationDeletedRemovesRow(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	ctx := context.Background()

	id, _ := e.db.CreateGithubAppInstallation(ctx, store.GithubAppInstallation{
		AppID: e.app.ID, InstallationID: 555, AccountLogin: "acme",
	})
	_ = id

	payload := map[string]any{
		"action": "deleted",
		"installation": map[string]any{
			"id": 555, "app_id": e.app.AppID,
			"account": map[string]any{"login": "acme", "id": 7},
		},
	}
	resp := e.post(t, "installation", "delivery-3", payload)
	resp.Body.Close()

	if _, err := e.db.GetGithubAppInstallationByInstallationID(ctx, 555); err == nil {
		t.Error("installation still present after delete")
	}
}

func TestWebhook_InstallationRepositoriesAddedAndRemoved(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	ctx := context.Background()

	instID, _ := e.db.CreateGithubAppInstallation(ctx, store.GithubAppInstallation{
		AppID: e.app.ID, InstallationID: 555, AccountLogin: "acme",
	})
	_, _ = e.db.AddGithubAppRepo(ctx, store.GithubAppRepo{
		InstallationID: instID, RepoID: 99, FullName: "acme/old",
	})

	payload := map[string]any{
		"action": "added",
		"installation": map[string]any{
			"id": 555, "app_id": e.app.AppID,
			"account": map[string]any{"login": "acme", "id": 7},
		},
		"repositories_added": []map[string]any{
			{"id": 100, "full_name": "acme/new"},
		},
		"repositories_removed": []map[string]any{
			{"id": 99, "full_name": "acme/old"},
		},
	}
	resp := e.post(t, "installation_repositories", "delivery-4", payload)
	resp.Body.Close()

	repos, _ := e.db.ListGithubReposForInstallation(ctx, instID)
	if len(repos) != 1 || repos[0].FullName != "acme/new" {
		t.Errorf("repos after webhook: %+v", repos)
	}
}

func TestWebhook_UnhandledEventIsNoOp(t *testing.T) {
	t.Parallel()
	e := newWebhookEnv(t)
	resp := e.post(t, "ping", "delivery-5", map[string]any{"zen": "Speak like a human."})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

// Anchor the cobaltapi import so removing webhook.go's references
// doesn't accidentally drop coverage.
var _ = cobaltapi.GithubApp{}
