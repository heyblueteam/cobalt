package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// TokenProvider returns a fresh-or-cached GitHub installation token for a
// project. Implementations must be safe for concurrent use.
type TokenProvider interface {
	GetInstallationToken(ctx context.Context, project store.Project) (github.InstallationToken, error)
}

// TokenStore is the subset of *store.DB the token provider needs. Defined
// as an interface so tests can substitute a fake.
type TokenStore interface {
	GetGithubAppInstallation(ctx context.Context, id int64) (*store.GithubAppInstallation, error)
	GetGithubApp(ctx context.Context, id int64) (*store.GithubApp, error)
	SetInstallationToken(ctx context.Context, id int64, token string, expiresAtUnix int64) error
}

// TokenMinter is the subset of *github.Client the token provider needs.
type TokenMinter interface {
	MintInstallationToken(ctx context.Context, jwt string, installationID int64) (github.InstallationToken, error)
}

// NewDBTokenProvider returns a TokenProvider that reads / writes the
// access_token columns on github_app_installations and mints fresh tokens
// via the supplied minter when the cached one is stale.
//
// nowFn defaults to time.Now if nil; tests use a fake clock.
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

// GetInstallationToken first checks the cached token on the installation
// row; if it's still valid (with our 5-minute refresh margin), returns it.
// Otherwise mints a fresh one via the GitHub API, persists it, and
// returns it.
func (p *dbTokenProvider) GetInstallationToken(ctx context.Context, project store.Project) (github.InstallationToken, error) {
	if !project.GithubAppInstallationID.Valid {
		return github.InstallationToken{}, errors.New("deploy: project has no GitHub App installation")
	}
	inst, err := p.store.GetGithubAppInstallation(ctx, project.GithubAppInstallationID.Int64)
	if err != nil {
		return github.InstallationToken{}, fmt.Errorf("deploy: lookup installation: %w", err)
	}

	// Cached token still valid?
	if inst.AccessToken.Valid && inst.AccessTokenExpiresAt.Valid {
		cached := github.InstallationToken{
			Token:     inst.AccessToken.String,
			ExpiresAt: time.Unix(inst.AccessTokenExpiresAt.Int64, 0),
		}
		if cached.Valid(p.now()) {
			return cached, nil
		}
	}

	// Mint a fresh one. App JWT lives 30 seconds; sign it with the App's
	// private key and call /app/installations/{id}/access_tokens.
	app, err := p.store.GetGithubApp(ctx, inst.AppID)
	if err != nil {
		return github.InstallationToken{}, fmt.Errorf("deploy: lookup app: %w", err)
	}
	jwt, err := github.SignAppJWT(app.AppID, app.PrivateKey, p.now())
	if err != nil {
		return github.InstallationToken{}, fmt.Errorf("deploy: sign jwt: %w", err)
	}
	tok, err := p.minter.MintInstallationToken(ctx, jwt, inst.InstallationID)
	if err != nil {
		return github.InstallationToken{}, fmt.Errorf("deploy: mint token: %w", err)
	}
	if err := p.store.SetInstallationToken(ctx, inst.ID, tok.Token, tok.ExpiresAt.Unix()); err != nil {
		// Cache write failure is logged but not fatal — we still got a
		// usable token.
		// (Intentionally swallow the error; production code can wrap
		// with a logger if visibility is needed.)
		_ = err
	}
	return tok, nil
}
