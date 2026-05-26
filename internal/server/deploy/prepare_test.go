package deploy

import (
	"context"
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

// ptrString returns &s. Used in tests to populate the *string nullable
// fields on store.Deployment (CommitSHA, CobaltfileOverride, ResolvedCobaltfile).
func ptrString(s string) *string { return &s }

// fakeGit records every git invocation and lets tests inject canned
// stdout / errors per command.
type fakeGit struct {
	mu     sync.Mutex
	calls  [][]string
	stdout map[string]string
	errs   map[string]error
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

	_, err := p.Prepare(
		context.Background(),
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
	_, err := p.Prepare(
		context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api", Branch: "main"},
		store.Deployment{CommitSHA: ptrString("specific-sha")},
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

// TestPreparer_PublicRepoUsesAnonymousURL: a project whose token
// provider returns the zero token (no installation grants access)
// must clone via the unauthenticated GitHub URL — never one with an
// empty `x-access-token:` placeholder.
func TestPreparer_PublicRepoUsesAnonymousURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newFakeGit()
	g.stdout["rev-parse"] = "abc"

	tok := &fakeTokenProvider{} // zero token = anonymous signal
	p := NewPreparer(dir, tok, g)

	repoDir := filepath.Join(dir, "projects", "api", "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Prepare(
		context.Background(),
		store.Project{Name: "api", GithubRepo: "public/repo", Branch: "main"},
		store.Deployment{},
	); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	cloneCalls := g.callsFor("clone")
	if len(cloneCalls) != 1 {
		t.Fatalf("expected 1 clone call, got %d", len(cloneCalls))
	}
	url := cloneCalls[0][2] // [dir, "clone", url, dest]
	if strings.Contains(url, "x-access-token") {
		t.Errorf("anonymous clone leaked auth placeholder: %q", url)
	}
	if !strings.HasPrefix(url, "https://github.com/public/repo") {
		t.Errorf("clone URL: got %q, want https://github.com/public/repo*", url)
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
	ws, err := p.Prepare(
		context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api", Branch: "main"},
		store.Deployment{CobaltfileOverride: ptrString(`{"version":"1.0","services":{"web":{"port":9999}}}`)},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if ws.Cobaltfile.Services["web"].Port != 9999 {
		t.Errorf("override ignored: port=%d", ws.Cobaltfile.Services["web"].Port)
	}
}

// TestPreparer_ReadsCobaltfileFromProjectPath proves that a project
// with a non-empty Path resolves its cobalt.json under that sub-path
// inside the cloned repo. The returned Workspace.Path points at the
// project root (`<repo>/<path>`) so the build phase joins Dockerfile +
// Context relative to that, not the repo root — making monorepo
// sub-deploys work transparently.
//
// Failure modes guarded against:
//   - Preparer reads `<repo>/cobalt.json` instead of `<repo>/<path>/cobalt.json`
//     → returns the wrong cobaltfile (or default if none exists at root)
//   - Workspace.Path points at the repo root → Build resolves Dockerfile
//     paths against the wrong tree
func TestPreparer_ReadsCobaltfileFromProjectPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newFakeGit()
	g.stdout["rev-parse"] = "abc"
	tok := &fakeTokenProvider{tok: github.InstallationToken{Token: "x", ExpiresAt: time.Now().Add(time.Hour)}}

	repoDir := filepath.Join(dir, "projects", "monorepo", "repo")
	_ = os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	// Cobaltfile at the root would be picked up if Path were ignored —
	// give it a distinguishing port so the test catches that bug.
	_ = os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{"port":1111}}}`), 0o644)
	// The "real" cobaltfile is under services/api/.
	subdir := filepath.Join(repoDir, "services", "api")
	_ = os.MkdirAll(subdir, 0o755)
	_ = os.WriteFile(filepath.Join(subdir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{"port":4000}}}`), 0o644)

	p := NewPreparer(dir, tok, g)
	ws, err := p.Prepare(
		context.Background(),
		store.Project{Name: "monorepo", GithubRepo: "h/monorepo", Branch: "main", Path: "services/api"},
		store.Deployment{},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := ws.Cobaltfile.Services["web"].Port; got != 4000 {
		t.Errorf("Cobaltfile.Services[web].Port = %d, want 4000 (subdir cobalt.json) — got %d would indicate root cobalt.json was read", got, got)
	}
	if !strings.HasSuffix(ws.Path, filepath.Join("services", "api")) {
		t.Errorf("Workspace.Path = %q, want suffix services/api so the build resolves Dockerfile relative to the subdir", ws.Path)
	}
}

// TestPreparer_EmptyPathIsRepoRoot pins the no-regression case: a
// project with empty Path keeps the old behavior — Workspace.Path is
// the repo root and the root cobalt.json is read.
func TestPreparer_EmptyPathIsRepoRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newFakeGit()
	g.stdout["rev-parse"] = "abc"
	tok := &fakeTokenProvider{tok: github.InstallationToken{Token: "x", ExpiresAt: time.Now().Add(time.Hour)}}

	repoDir := filepath.Join(dir, "projects", "api", "repo")
	_ = os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(repoDir, "cobalt.json"),
		[]byte(`{"version":"1.0","services":{"web":{"port":3000}}}`), 0o644)

	p := NewPreparer(dir, tok, g)
	ws, err := p.Prepare(
		context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api", Branch: "main"}, // no Path
		store.Deployment{},
	)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if ws.Path != repoDir {
		t.Errorf("Workspace.Path = %q, want repo root %q", ws.Path, repoDir)
	}
	if got := ws.Cobaltfile.Services["web"].Port; got != 3000 {
		t.Errorf("Cobaltfile port = %d, want 3000", got)
	}
}

func TestPreparer_PropagatesTokenError(t *testing.T) {
	t.Parallel()
	tok := &fakeTokenProvider{err: errors.New("no token")}
	p := NewPreparer(t.TempDir(), tok, newFakeGit())
	_, err := p.Prepare(
		context.Background(),
		store.Project{Name: "api", GithubRepo: "h/api"},
		store.Deployment{},
	)
	if err == nil {
		t.Error("expected error")
	}
}
