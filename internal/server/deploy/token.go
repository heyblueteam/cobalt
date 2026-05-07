package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// TokenProvider resolves a GitHub installation token for a project's repo.
//
// Returns a token whose `Token == ""` to signal "no installation grants
// access — fall back to anonymous clone." A non-nil error is reserved for
// genuine infrastructure failures (DB unavailable, etc.); GitHub-side
// rejections of individual candidates are absorbed and tried-next.
//
// Implementations must be safe for concurrent use.
type TokenProvider interface {
	GetInstallationToken(ctx context.Context, project store.Project) (github.InstallationToken, error)
}

// TokenStore is the subset of *store.DB the token provider needs. Defined
// as an interface so tests can substitute a fake.
type TokenStore interface {
	ListGithubReposByFullName(ctx context.Context, fullName string) ([]store.GithubAppRepo, error)
	GetGithubAppInstallation(ctx context.Context, id int64) (*store.GithubAppInstallation, error)
	GetGithubApp(ctx context.Context, id int64) (*store.GithubApp, error)
	SetInstallationToken(ctx context.Context, id int64, token string, expiresAtUnix int64) error
}

// TokenMinter is the subset of *github.Client the token provider needs.
// Two operations: mint a fresh installation token, and list the repos an
// installation token can access (used for the post-mint verify step).
type TokenMinter interface {
	MintInstallationToken(ctx context.Context, jwt string, installationID int64) (github.InstallationToken, error)
	ListInstallationRepos(ctx context.Context, installationToken string) ([]github.Repository, error)
}

// NewDBTokenProvider returns a TokenProvider that resolves repo →
// installation by joining `github_app_repos` on full_name, mints/caches
// installation tokens, and verifies each token's repo access against
// GitHub before handing it back. nowFn defaults to time.Now if nil.
func NewDBTokenProvider(s TokenStore, m TokenMinter, nowFn func() time.Time) TokenProvider {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &dbTokenProvider{store: s, minter: m, now: nowFn}
}

type dbTokenProvider struct {
	store  TokenStore
	minter TokenMinter
	now    func() time.Time
}

// GetInstallationToken walks every installation that claims access to
// project.GithubRepo (most-recently-refreshed first) and returns the
// first token that GitHub still confirms grants access to the repo. If
// no candidate works — including the case where no installation knows
// about the repo at all — returns the zero token, no error: caller
// falls back to an anonymous clone, which will succeed iff the repo is
// public.
func (p *dbTokenProvider) GetInstallationToken(ctx context.Context, project store.Project) (github.InstallationToken, error) {
	if project.GithubRepo == "" {
		return github.InstallationToken{}, errors.New("deploy: project has no github repo")
	}

	repos, err := p.store.ListGithubReposByFullName(ctx, project.GithubRepo)
	if err != nil {
		return github.InstallationToken{}, fmt.Errorf("deploy: lookup repo candidates: %w", err)
	}
	if len(repos) == 0 {
		// No installation has registered this repo. Anonymous clone.
		return github.InstallationToken{}, nil
	}

	for _, repo := range repos {
		tok, ok := p.tryCandidate(ctx, repo, project.GithubRepo)
		if ok {
			return tok, nil
		}
	}
	// Every candidate failed (mint or verify). Fall back to anonymous —
	// the repo is either public (anonymous succeeds) or git fetch will
	// fail with a clear error.
	return github.InstallationToken{}, nil
}

// tryCandidate attempts to obtain a verified-good installation token via
// the supplied github_app_repos row. Returns the token and ok=true on
// success, zero token and ok=false on any failure (DB, mint, verify, or
// repo-not-in-list). Per-candidate failures are logged but never
// propagated — the resolver moves on to the next candidate.
func (p *dbTokenProvider) tryCandidate(ctx context.Context, repo store.GithubAppRepo, fullName string) (github.InstallationToken, bool) {
	inst, err := p.store.GetGithubAppInstallation(ctx, repo.InstallationID)
	if err != nil {
		slog.WarnContext(ctx, "token: lookup installation", "repo", fullName, "installation", repo.InstallationID, "error", err)
		return github.InstallationToken{}, false
	}

	tok, ok := p.tokenForInstallation(ctx, inst)
	if !ok {
		return github.InstallationToken{}, false
	}

	// Defense-in-depth: webhooks should keep github_app_repos current,
	// but a missed webhook (or a race) could leave us about to hand back
	// a token that GitHub no longer considers authorized for this repo.
	// One round trip rules that out before we embed the token in a clone
	// URL the deploy will retry against.
	githubRepos, err := p.minter.ListInstallationRepos(ctx, tok.Token)
	if err != nil {
		slog.WarnContext(ctx, "token: verify list-repos", "repo", fullName, "installation", inst.InstallationID, "error", err)
		return github.InstallationToken{}, false
	}
	for _, r := range githubRepos {
		if r.FullName == fullName {
			return tok, true
		}
	}
	slog.InfoContext(ctx, "token: installation no longer has access", "repo", fullName, "installation", inst.InstallationID)
	return github.InstallationToken{}, false
}

// tokenForInstallation returns a fresh-or-cached installation token for
// inst. Returns ok=false on mint failure (treated as "skip this
// candidate"); the cache-write failure path is non-fatal.
func (p *dbTokenProvider) tokenForInstallation(ctx context.Context, inst *store.GithubAppInstallation) (github.InstallationToken, bool) {
	if inst.AccessToken.Valid && inst.AccessTokenExpiresAt.Valid {
		cached := github.InstallationToken{
			Token:     inst.AccessToken.String,
			ExpiresAt: time.Unix(inst.AccessTokenExpiresAt.Int64, 0),
		}
		if cached.Valid(p.now()) {
			return cached, true
		}
	}

	app, err := p.store.GetGithubApp(ctx, inst.AppID)
	if err != nil {
		slog.WarnContext(ctx, "token: lookup app", "installation", inst.InstallationID, "error", err)
		return github.InstallationToken{}, false
	}
	jwt, err := github.SignAppJWT(app.AppID, app.PrivateKey, p.now())
	if err != nil {
		slog.WarnContext(ctx, "token: sign jwt", "app", app.AppID, "error", err)
		return github.InstallationToken{}, false
	}
	tok, err := p.minter.MintInstallationToken(ctx, jwt, inst.InstallationID)
	if err != nil {
		slog.WarnContext(ctx, "token: mint", "installation", inst.InstallationID, "error", err)
		return github.InstallationToken{}, false
	}
	if err := p.store.SetInstallationToken(ctx, inst.ID, tok.Token, tok.ExpiresAt.Unix()); err != nil {
		slog.WarnContext(ctx, "token: cache write", "installation", inst.InstallationID, "error", err)
		// Non-fatal — we still got a usable token.
	}
	return tok, true
}
