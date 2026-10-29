// Package deploynotify turns a finished deployment into a notification. It
// reads the project's resolved cobaltfile, resolves the merged pull request
// for the deployed commit, and hands a rendered message to a provider.
//
// It is intentionally outside the deploy package: deploy owns the state
// machine, this package owns message composition. The dispatcher talks to it
// through deploy.DeploymentNotifier.
package deploynotify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/notify"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// TokenProvider is the GitHub installation-token subset this package needs.
// deploy.TokenProvider satisfies it.
type TokenProvider interface {
	GetInstallationToken(ctx context.Context, project store.Project) (github.InstallationToken, error)
}

// Notifier composes deploy messages. A nil Telegram disables the package.
type Notifier struct {
	DB       *store.DB
	GitHub   *github.Client
	Tokens   TokenProvider
	Telegram *notify.Telegram
	DataDir  string
	Host     string
	Log      *slog.Logger
}

// Notify sends one message for a terminal deployment. Errors are for the
// caller's log; the deployment result never depends on them.
func (n *Notifier) Notify(ctx context.Context, dep store.Deployment, status cobaltapi.State, runErr error) error {
	if n == nil || n.Telegram == nil {
		return nil
	}
	project, err := n.DB.GetProjectByID(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("notify: load project: %w", err)
	}
	// Re-read the row so finished_at and the resolved cobaltfile are present.
	// The dispatcher's copy was loaded before the deploy ran.
	if fresh, err := n.DB.GetDeployment(ctx, dep.ID); err == nil {
		dep = *fresh
	}
	cf, err := n.cobaltfileFor(ctx, dep)
	if err != nil {
		return err
	}
	if cf == nil || cf.Notify == nil || !cf.Notify.Telegram.IsEnabled() {
		return nil
	}
	tg := cf.Notify.Telegram

	templatePath, err := n.templatePath(project, tg.TemplateFile)
	if err != nil {
		return err
	}

	prNumber, prURL := n.pullRequest(ctx, project, dep)

	ev := notify.Event{
		Status:    eventStatus(status),
		Project:   project.Name,
		Number:    dep.Number,
		Repo:      project.GithubRepo,
		Branch:    project.Branch,
		CommitSHA: shaOrEmpty(dep.CommitSHA),
		PRNumber:  prNumber,
		PRURL:     prURL,
		Duration:  duration(dep),
		Host:      n.Host,
		Domains:   n.domains(ctx, project.ID),
		Error:     errorText(runErr),
	}
	if dep.CommitSHA != nil && *dep.CommitSHA != "" {
		ev.ShortSHA = shortSHA(*dep.CommitSHA)
		ev.CommitURL = commitURL(project.GithubRepo, *dep.CommitSHA)
	}
	if dep.StartedAt != nil {
		ev.StartedAt = time.Unix(*dep.StartedAt, 0)
	}
	if dep.FinishedAt != nil {
		ev.FinishedAt = time.Unix(*dep.FinishedAt, 0)
	}

	return n.Telegram.Notify(ctx, notify.Config{
		Chat:         tg.Chat,
		Events:       tg.On,
		TemplateFile: templatePath,
	}, ev)
}

// cobaltfileFor returns the deployment's resolved cobaltfile, falling back to
// the most recent deployment that has one. That fallback matters when a
// deploy fails during fetch, before this deployment's cobaltfile is stored.
func (n *Notifier) cobaltfileFor(ctx context.Context, dep store.Deployment) (*cobaltfile.Cobaltfile, error) {
	if cf, ok := parseResolved(dep.ResolvedCobaltfile); ok {
		return cf, nil
	}
	deps, err := n.DB.ListDeploymentsForProject(ctx, dep.ProjectID, 20)
	if err != nil {
		return nil, fmt.Errorf("notify: load recent deployments: %w", err)
	}
	for i := range deps {
		if cf, ok := parseResolved(deps[i].ResolvedCobaltfile); ok {
			return cf, nil
		}
	}
	return nil, nil
}

func parseResolved(raw *string) (*cobaltfile.Cobaltfile, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, false
	}
	cf, err := cobaltfile.Parse([]byte(*raw))
	if err != nil {
		return nil, false
	}
	return cf, true
}

// templatePath resolves templateFile against the project root inside the
// deploy checkout and refuses to leave it. An empty name means "use the
// built-in default".
func (n *Notifier) templatePath(project *store.Project, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	root := filepath.Join(n.DataDir, "projects", project.Name, "repo", project.Path)
	full := filepath.Join(root, name)
	if !within(root, full) {
		return "", fmt.Errorf("notify: templateFile %q leaves the project directory", name)
	}
	// A symlink can point outside the checkout even when the path looks
	// local. Resolve it when the file exists and check again.
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil && !within(resolvedRoot, resolved) {
			return "", fmt.Errorf("notify: templateFile %q resolves outside the project directory", name)
		}
	}
	return full, nil
}

// within reports whether path is root or a descendant of root.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func (n *Notifier) pullRequest(ctx context.Context, project *store.Project, dep store.Deployment) (int, string) {
	if n.GitHub == nil || n.Tokens == nil || dep.CommitSHA == nil || *dep.CommitSHA == "" {
		return 0, ""
	}
	tok, err := n.Tokens.GetInstallationToken(ctx, *project)
	if err != nil || tok.Token == "" {
		return 0, ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	prs, err := n.GitHub.ListPullRequestsForCommit(lookupCtx, tok.Token, project.GithubRepo, *dep.CommitSHA)
	if err != nil {
		if n.Log != nil {
			n.Log.Warn("notify: pull request lookup failed", "repo", project.GithubRepo, "error", err)
		}
		return 0, ""
	}
	for _, pr := range prs {
		if pr.MergedAt != nil {
			return pr.Number, pr.HTMLURL
		}
	}
	return 0, ""
}

func (n *Notifier) domains(ctx context.Context, projectID int64) []string {
	domains, err := n.DB.ListDomainsForProject(ctx, projectID)
	if err != nil {
		return nil
	}
	return domains
}

// eventStatus maps cobalt's terminal states to notification event names.
// Cobalt says "failed"; the config and templates say "failure".
func eventStatus(status cobaltapi.State) string {
	if status == cobaltapi.StateFailed {
		return cobaltfile.NotifyEventFailure
	}
	return string(status)
}

func duration(dep store.Deployment) time.Duration {
	if dep.StartedAt == nil || dep.FinishedAt == nil {
		return 0
	}
	return time.Duration(*dep.FinishedAt-*dep.StartedAt) * time.Second
}

func shaOrEmpty(sha *string) string {
	if sha == nil {
		return ""
	}
	return *sha
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func commitURL(repo, sha string) string {
	if repo == "" || sha == "" {
		return ""
	}
	return "https://github.com/" + repo + "/commit/" + sha
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	const max = 1000
	runes := []rune(err.Error())
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return string(runes)
}
