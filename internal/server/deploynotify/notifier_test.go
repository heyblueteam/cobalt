package deploynotify

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

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/notify"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/internal/storetest"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

type fakeTokens struct{ token string }

func (f fakeTokens) GetInstallationToken(context.Context, store.Project) (github.InstallationToken, error) {
	return github.InstallationToken{Token: f.token}, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func telegramRecorder(t *testing.T) (*notify.Telegram, *[]string) {
	t.Helper()
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		texts = append(texts, r.FormValue("text"))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return &notify.Telegram{
		Token:   "tok",
		APIBase: srv.URL,
		Client:  srv.Client(),
	}, &texts
}

func githubRecorder(t *testing.T, merged bool) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/commits/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !merged {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"number":4310,"html_url":"https://github.com/heyblueteam/blue/pull/4310","merged":true,"merged_at":"2026-10-29T10:00:00Z"}]`))
	}))
	t.Cleanup(srv.Close)
	return github.NewClientWithBaseURL(srv.URL, srv.Client())
}

// seedDeployment creates a project and one deployment with a resolved
// cobaltfile. Returns the project and deployment.
func seedDeployment(t *testing.T, db *store.DB, cobaltfileJSON string) (*store.Project, store.Deployment) {
	t.Helper()
	ctx := context.Background()
	pid, err := db.CreateProject(ctx, store.Project{
		Name: "api", GithubRepo: "heyblueteam/blue", Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project, err := db.GetProjectByID(ctx, pid)
	if err != nil {
		t.Fatalf("GetProjectByID: %v", err)
	}
	number, err := db.NextDeploymentNumber(ctx, pid)
	if err != nil {
		t.Fatalf("NextDeploymentNumber: %v", err)
	}
	sha := "4f2c9a1b2c3d4e5f60718293a4b5c6d7e8f90123"
	id, err := db.CreateDeployment(ctx, store.Deployment{
		ProjectID: pid,
		Number:    number,
		Status:    cobaltapi.StateQueued,
		CommitSHA: &sha,
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if cobaltfileJSON != "" {
		if err := db.SetResolvedCobaltfile(ctx, id, cobaltfileJSON); err != nil {
			t.Fatalf("SetResolvedCobaltfile: %v", err)
		}
	}
	if err := db.SetDeploymentStatus(ctx, id, cobaltapi.StateSuccess); err != nil {
		t.Fatalf("SetDeploymentStatus: %v", err)
	}
	dep, err := db.GetDeployment(ctx, id)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	return project, *dep
}

func TestNotifier_SendsForEnabledProject(t *testing.T) {
	t.Parallel()
	db := storetest.OpenDB(t)
	telegram, texts := telegramRecorder(t)
	dataDir := t.TempDir()
	_, dep := seedDeployment(t, db, `{
		"version": "1.0",
		"notify": {"telegram": {"chat": "-100", "on": ["success", "failure"]}}
	}`)

	n := &Notifier{
		DB:       db,
		GitHub:   githubRecorder(t, true),
		Tokens:   fakeTokens{token: "install"},
		Telegram: telegram,
		DataDir:  dataDir,
		Host:     "server.blue.cc",
		Log:      quietLogger(),
	}
	if err := n.Notify(context.Background(), dep, cobaltapi.StateSuccess, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*texts) != 1 {
		t.Fatalf("messages: got %d, want 1", len(*texts))
	}
	got := (*texts)[0]
	for _, want := range []string{"api", "PR #4310", "heyblueteam/blue", "server.blue.cc"} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
}

func TestNotifier_SkipsDisabledProject(t *testing.T) {
	t.Parallel()
	db := storetest.OpenDB(t)
	telegram, texts := telegramRecorder(t)
	_, dep := seedDeployment(t, db, `{
		"version": "1.0",
		"notify": {"telegram": {"enabled": false, "chat": "-100"}}
	}`)

	n := &Notifier{DB: db, Telegram: telegram, DataDir: t.TempDir(), Log: quietLogger()}
	if err := n.Notify(context.Background(), dep, cobaltapi.StateSuccess, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*texts) != 0 {
		t.Fatalf("messages: got %d, want 0", len(*texts))
	}
}

func TestNotifier_SkipsProjectWithoutNotifyBlock(t *testing.T) {
	t.Parallel()
	db := storetest.OpenDB(t)
	telegram, texts := telegramRecorder(t)
	_, dep := seedDeployment(t, db, `{"version": "1.0"}`)

	n := &Notifier{DB: db, Telegram: telegram, DataDir: t.TempDir(), Log: quietLogger()}
	if err := n.Notify(context.Background(), dep, cobaltapi.StateSuccess, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*texts) != 0 {
		t.Fatalf("messages: got %d, want 0", len(*texts))
	}
}

func TestNotifier_FallsBackToPreviousResolvedCobaltfile(t *testing.T) {
	t.Parallel()
	db := storetest.OpenDB(t)
	telegram, texts := telegramRecorder(t)
	ctx := context.Background()

	project, _ := seedDeployment(t, db, `{
		"version": "1.0",
		"notify": {"telegram": {"chat": "-100", "on": ["failure"]}}
	}`)
	number, err := db.NextDeploymentNumber(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedID, err := db.CreateDeployment(ctx, store.Deployment{
		ProjectID: project.ID,
		Number:    number,
		Status:    cobaltapi.StateQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeploymentStatus(ctx, failedID, cobaltapi.StateFailed); err != nil {
		t.Fatal(err)
	}
	failed, err := db.GetDeployment(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}

	n := &Notifier{DB: db, Telegram: telegram, DataDir: t.TempDir(), Log: quietLogger()}
	if err := n.Notify(ctx, *failed, cobaltapi.StateFailed, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*texts) != 1 {
		t.Fatalf("messages: got %d, want 1 (fallback config)", len(*texts))
	}
}

func TestNotifier_RejectsTemplateOutsideProject(t *testing.T) {
	t.Parallel()
	n := &Notifier{DataDir: t.TempDir()}
	// Config validation rejects this first; the resolver repeats the check
	// because it is the last line between a repo path and the filesystem.
	if _, err := n.templatePath(&store.Project{Name: "api"}, "../escape.tmpl"); err == nil {
		t.Fatal("expected an error for a path outside the project")
	}
}

func TestNotifier_UsesTemplateFile(t *testing.T) {
	t.Parallel()
	db := storetest.OpenDB(t)
	telegram, texts := telegramRecorder(t)
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "projects", "api", "repo", ".cobalt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "telegram.tmpl"), []byte(`custom {{.Project}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, dep := seedDeployment(t, db, `{
		"version": "1.0",
		"notify": {"telegram": {"chat": "-100", "on": ["success"], "templateFile": ".cobalt/telegram.tmpl"}}
	}`)

	n := &Notifier{DB: db, Telegram: telegram, DataDir: dataDir, Log: quietLogger()}
	if err := n.Notify(context.Background(), dep, cobaltapi.StateSuccess, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := (*texts)[0]; got != "custom api" {
		t.Errorf("text: got %q", got)
	}
}
