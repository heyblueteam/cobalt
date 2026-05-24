package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// Workspace is the result of preparing a deployment: a cloned repo on
// disk, a parsed cobaltfile, and the resolved commit SHA.
type Workspace struct {
	Path       string
	Cobaltfile *cobaltfile.Cobaltfile
	Commit     string // 40-char SHA actually checked out
}

// Preparer fetches a project's source for a deployment and parses the
// cobalt.json. Implementations are responsible for keeping the workspace
// fresh (clone or fetch+checkout) and for honoring deployment-level
// overrides (commit hint, inline cobalt.json).
type Preparer interface {
	Prepare(ctx context.Context, project store.Project, dep store.Deployment) (*Workspace, error)
}

// GitRunner is the subset of `git` shell-out behavior the preparer uses.
// Tests substitute a fake.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) error
	Output(ctx context.Context, dir string, args ...string) (string, error)
}

// ExecGit is the production GitRunner: shells out to /usr/bin/git.
type ExecGit struct{}

// Run invokes git with the given args in dir. Stdout/stderr are
// discarded; non-zero exit produces an error containing the captured
// stderr.
func (ExecGit) Run(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	errOut, _ := io.ReadAll(stderr)
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(string(errOut))
		if msg != "" {
			return fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// Output invokes git and returns trimmed stdout.
func (ExecGit) Output(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitPreparer is the production Preparer: keeps a per-project repo on
// disk, refreshes it before each deploy, and parses the cobaltfile from
// the working tree.
type gitPreparer struct {
	dataDir string // root, e.g. /cobalt/data
	tokens  TokenProvider
	git     GitRunner
}

// NewPreparer returns a Preparer rooted at dataDir. Cloned repos live at
// {dataDir}/projects/{name}/repo.
func NewPreparer(dataDir string, tokens TokenProvider, git GitRunner) Preparer {
	if git == nil {
		git = ExecGit{}
	}
	return &gitPreparer{dataDir: dataDir, tokens: tokens, git: git}
}

func (p *gitPreparer) Prepare(ctx context.Context, project store.Project, dep store.Deployment) (*Workspace, error) {
	tok, err := p.tokens.GetInstallationToken(ctx, project)
	if err != nil {
		return nil, err
	}
	// Empty token = no installation grants access. Clone anonymously;
	// works iff the repo is public, otherwise git fails with a clear
	// "Repository not found" the user can act on.
	cloneURL := github.AnonymousCloneURL(project.GithubRepo)
	if tok.Token != "" {
		cloneURL = github.CloneURL(tok.Token, project.GithubRepo)
	}

	repoDir := filepath.Join(p.dataDir, "projects", project.Name, "repo")
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
		return nil, fmt.Errorf("deploy.Prepare: mkdir parent: %w", err)
	}

	exists, err := isGitRepo(repoDir)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := p.git.Run(ctx, "", "clone", cloneURL, repoDir); err != nil {
			return nil, fmt.Errorf("deploy.Prepare: clone: %w", err)
		}
	} else {
		// Always refresh the remote URL so a stale (expired) token from a
		// prior deploy doesn't break this one.
		if err := p.git.Run(ctx, repoDir, "remote", "set-url", "origin", cloneURL); err != nil {
			return nil, fmt.Errorf("deploy.Prepare: remote set-url: %w", err)
		}
		if err := p.git.Run(ctx, repoDir, "fetch", "--prune", "origin"); err != nil {
			return nil, fmt.Errorf("deploy.Prepare: fetch: %w", err)
		}
	}

	sha := ""
	if dep.CommitSHA != nil {
		sha = *dep.CommitSHA
	}
	target := strings.TrimSpace(sha)
	if target == "" {
		target = "origin/" + project.Branch
	}
	if err := p.git.Run(ctx, repoDir, "checkout", "--detach", target); err != nil {
		return nil, fmt.Errorf("deploy.Prepare: checkout %s: %w", target, err)
	}

	commit, err := p.git.Output(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("deploy.Prepare: rev-parse: %w", err)
	}

	override := ""
	if dep.CobaltfileOverride != nil && *dep.CobaltfileOverride != "" {
		override = *dep.CobaltfileOverride
	}
	// Project.Path scopes everything downstream — cobalt.json location,
	// Dockerfile resolution, build context. Empty Path == repo root.
	// The Workspace.Path returned here IS the project root from the
	// build's perspective; the build phase joins Image.Dockerfile and
	// Image.Context relative to it, unaware of whether the project
	// lives at the repo root or in a subtree.
	projectRoot := filepath.Join(repoDir, project.Path)
	cf, err := loadCobaltfile(projectRoot, override)
	if err != nil {
		return nil, err
	}
	return &Workspace{Path: projectRoot, Cobaltfile: cf, Commit: commit}, nil
}

// loadCobaltfile prefers an inline override (when --file was used at
// deploy time), otherwise reads cobalt.json from `projectRoot`, otherwise
// returns the default cobaltfile.
//
// `projectRoot` is the deploy's project root — `<repo>` for projects at
// the repo root (Project.Path == ""), or `<repo>/<Project.Path>` for
// monorepo sub-deployments.
func loadCobaltfile(projectRoot, override string) (*cobaltfile.Cobaltfile, error) {
	if override != "" {
		return cobaltfile.Parse([]byte(override))
	}
	return cobaltfile.ParseFile(filepath.Join(projectRoot, "cobalt.json"))
}

// isGitRepo reports whether dir is an existing git working tree (has a
// .git entry).
func isGitRepo(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("deploy.Prepare: stat .git: %w", err)
	}
	_ = info
	return true, nil
}
