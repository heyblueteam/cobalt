package deploy

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// fakeGit records every git invocation and lets tests inject canned
// stdout / errors per command.
type fakeGit struct {
	mu      sync.Mutex
	calls   [][]string
	stdout  map[string]string
	errs    map[string]error
	repoDir string
}

func newFakeGit() *fakeGit {
	return &fakeGit{stdout: map[string]string{}, errs: map[string]error{}}
}

func (f *fakeGit) Run(_ context.Context, dir string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{dir}, args...))
	if err := f.errs[args[0]]; err != nil {
		return err
	}
	// First-time clone: simulate creating .git in the dest dir.
	if args[0] == "clone" && len(args) == 3 {
		dest := args[2]
		_ = os.MkdirAll(filepath.Join(dest, ".git"), 0o755)
	}
	return nil
}

func (f *fakeGit) Output(_ context.Context, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{"output"}, args...))
	if err := f.errs[args[0]]; err != nil {
		return "", err
	}
	return f.stdout[args[0]], nil
}

func (f *fakeGit) callsFor(cmd string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		// c is [dir, cmd, args...]; cmd is at index 1 for Run, or "output"
		// for Output (with the real cmd at index 1).
		if len(c) >= 2 && c[1] == cmd {
			out = append(out, c)
		}
	}
	return out
}

type fakeTokenProvider struct {
	tok github.InstallationToken
	err error
}

func (f *fakeTokenProvider) GetInstallationToken(_ context.Context, _ store.Project) (github.InstallationToken, error) {
	if f.err != nil {
		return github.InstallationToken{}, f.err
	}
	return f.tok, nil
}

func TestPreparer_FreshClone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newFakeGit()
	g.stdout["rev-parse"] = "abc123def456"

	tok := &fakeTokenProvider{tok: github.InstallationToken{Token: "tok", ExpiresAt: time.Now().Add(time.Hour)}}
	p := NewPreparer(dir, tok, g)

	project := store.Project{ID: 1, Name: "api", GithubRepo: "h/api", Branch: "main"}
	dep := store.Deployment{ID: 1, ProjectID: 1, Number: 1}

	// Drop a cobalt.json that the post-checkout read will pick up.
	repoDir := filepath.Join(dir, "projects", "api", "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := p.Prepare(context.Background(), project, dep)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if ws.Commit != "abc123def456" {
		t.Errorf("Commit: %q", ws.Commit)
	}
	if ws.Cobaltfile == nil {
		t.Fatal("nil cobaltfile")
	}
	if _, ok := ws.Cobaltfile.Services["web"]; !ok {
		t.Error("missing web service")
	}

	cloneCalls := g.callsFor("clone")
	if len(cloneCalls) != 1 {
		t.Errorf("expected 1 clone call, got %d", len(cloneCalls))
	}
	if !strings.Contains(strings.Join(cloneCalls[0], " "), "tok") {
		t.Error("token not included in clone URL")
	}
}

func TestPreparer_ExistingRepoFetches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "projects", "api", "repo")
	_ = os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{}}}`), 0o644)

	g := newFakeGit()
	g.stdout["rev-parse"] = "deadbeef"
	tok := &fakeTokenProvider{tok: github.InstallationToken{Token: "x", ExpiresAt: time.Now().Add(time.Hour)}}
	p := NewPreparer(dir, tok, g)

	_, err := p.Prepare(context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api", Branch: "main"},
		store.Deployment{},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Should have called remote set-url and fetch, but NOT clone.
	if len(g.callsFor("clone")) != 0 {
		t.Error("clone called even though repo exists")
	}
	if len(g.callsFor("remote")) == 0 {
		t.Error("remote set-url not called")
	}
	if len(g.callsFor("fetch")) == 0 {
		t.Error("fetch not called")
	}
}

func TestPreparer_HonorsCommitOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newFakeGit()
	g.stdout["rev-parse"] = "abc"
	tok := &fakeTokenProvider{tok: github.InstallationToken{Token: "x", ExpiresAt: time.Now().Add(time.Hour)}}

	repoDir := filepath.Join(dir, "projects", "api", "repo")
	_ = os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{}}}`), 0o644)

	p := NewPreparer(dir, tok, g)
	_, err := p.Prepare(context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api", Branch: "main"},
		store.Deployment{CommitSHA: sql.NullString{String: "specific-sha", Valid: true}},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	checkouts := g.callsFor("checkout")
	if len(checkouts) != 1 {
		t.Fatalf("checkout calls: %v", checkouts)
	}
	last := checkouts[0]
	if last[len(last)-1] != "specific-sha" {
		t.Errorf("checkout target: got %q, want specific-sha", last[len(last)-1])
	}
}

func TestPreparer_HonorsCobaltfileOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newFakeGit()
	g.stdout["rev-parse"] = "abc"
	tok := &fakeTokenProvider{tok: github.InstallationToken{Token: "x", ExpiresAt: time.Now().Add(time.Hour)}}

	repoDir := filepath.Join(dir, "projects", "api", "repo")
	_ = os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	// Repo has its own cobalt.json with port 4000.
	_ = os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{"port":4000}}}`), 0o644)

	p := NewPreparer(dir, tok, g)
	ws, err := p.Prepare(context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api", Branch: "main"},
		store.Deployment{CobaltfileOverride: sql.NullString{
			String: `{"version":"1.0","services":{"web":{"port":9999}}}`, Valid: true,
		}},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if ws.Cobaltfile.Services["web"].Port != 9999 {
		t.Errorf("override ignored: port=%d", ws.Cobaltfile.Services["web"].Port)
	}
}

func TestPreparer_PropagatesTokenError(t *testing.T) {
	t.Parallel()
	tok := &fakeTokenProvider{err: errors.New("no token")}
	p := NewPreparer(t.TempDir(), tok, newFakeGit())
	_, err := p.Prepare(context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api"},
		store.Deployment{},
	)
	if err == nil {
		t.Error("expected error")
	}
}
