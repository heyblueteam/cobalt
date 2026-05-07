package deploy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

func generateRSA(t *testing.T) string {
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

// fakeTokenStore is a hand-rolled TokenStore used by every test in this
// file. Keep it permissive — tests set only the fields they care about.
type fakeTokenStore struct {
	repos         []store.GithubAppRepo
	installations map[int64]*store.GithubAppInstallation // keyed by local PK
	apps          map[int64]*store.GithubApp             // keyed by local PK
	saved         struct {
		token     string
		expiresAt int64
	}
}

func (f *fakeTokenStore) ListGithubReposByFullName(_ context.Context, _ string) ([]store.GithubAppRepo, error) {
	return f.repos, nil
}
func (f *fakeTokenStore) GetGithubAppInstallation(_ context.Context, id int64) (*store.GithubAppInstallation, error) {
	if inst, ok := f.installations[id]; ok {
		return inst, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeTokenStore) GetGithubApp(_ context.Context, id int64) (*store.GithubApp, error) {
	if app, ok := f.apps[id]; ok {
		return app, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeTokenStore) SetInstallationToken(_ context.Context, _ int64, token string, exp int64) error {
	f.saved.token = token
	f.saved.expiresAt = exp
	return nil
}

// fakeMinter records mint calls and returns canned tokens / repo lists.
// listRepos is keyed by token so tests can vary the verify outcome per
// candidate.
type fakeMinter struct {
	mintCalls int
	tok       github.InstallationToken
	mintErr   error

	listCalls int
	listRepos map[string][]github.Repository // tokenString → repos returned
	listErr   map[string]error               // tokenString → error
}

func (m *fakeMinter) MintInstallationToken(_ context.Context, _ string, _ int64) (github.InstallationToken, error) {
	m.mintCalls++
	if m.mintErr != nil {
		return github.InstallationToken{}, m.mintErr
	}
	return m.tok, nil
}

func (m *fakeMinter) ListInstallationRepos(_ context.Context, token string) ([]github.Repository, error) {
	m.listCalls++
	if err, ok := m.listErr[token]; ok {
		return nil, err
	}
	return m.listRepos[token], nil
}

// TestTokenProvider_UsesCacheWhenValid: cached token is fresh, verify
// confirms the repo is still in the installation's set, so we hand it
// back without minting.
func TestTokenProvider_UsesCacheWhenValid(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	expires := now.Add(time.Hour)
	st := &fakeTokenStore{
		repos: []store.GithubAppRepo{{InstallationID: 1, FullName: "h/api"}},
		installations: map[int64]*store.GithubAppInstallation{
			1: {
				ID: 1, AppID: 1, InstallationID: 999,
				AccessToken:          sql.NullString{String: "cached", Valid: true},
				AccessTokenExpiresAt: sql.NullInt64{Int64: expires.Unix(), Valid: true},
			},
		},
	}
	m := &fakeMinter{
		listRepos: map[string][]github.Repository{
			"cached": {{FullName: "h/api"}},
		},
	}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	tok, err := p.GetInstallationToken(context.Background(),
		store.Project{GithubRepo: "h/api"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "cached" {
		t.Errorf("token: got %q, want cached", tok.Token)
	}
	if m.mintCalls != 0 {
		t.Errorf("mint called %d times, want 0", m.mintCalls)
	}
	if m.listCalls != 1 {
		t.Errorf("verify called %d times, want 1", m.listCalls)
	}
}

// TestTokenProvider_MintsWhenCacheStale: cache is within the refresh
// margin → mint a fresh token, verify, persist, return it.
func TestTokenProvider_MintsWhenCacheStale(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	st := &fakeTokenStore{
		repos: []store.GithubAppRepo{{InstallationID: 1, FullName: "h/api"}},
		apps: map[int64]*store.GithubApp{
			1: {ID: 1, AppID: 12345, PrivateKey: generateRSA(t)},
		},
		installations: map[int64]*store.GithubAppInstallation{
			1: {
				ID: 1, AppID: 1, InstallationID: 999,
				AccessToken:          sql.NullString{String: "stale", Valid: true},
				AccessTokenExpiresAt: sql.NullInt64{Int64: now.Add(time.Minute).Unix(), Valid: true},
			},
		},
	}
	freshExpires := now.Add(time.Hour)
	m := &fakeMinter{
		tok: github.InstallationToken{Token: "fresh", ExpiresAt: freshExpires},
		listRepos: map[string][]github.Repository{
			"fresh": {{FullName: "h/api"}},
		},
	}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	tok, err := p.GetInstallationToken(context.Background(),
		store.Project{GithubRepo: "h/api"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "fresh" {
		t.Errorf("token: got %q, want fresh", tok.Token)
	}
	if m.mintCalls != 1 {
		t.Errorf("mint called %d times, want 1", m.mintCalls)
	}
	if st.saved.token != "fresh" {
		t.Errorf("cache write: got %q, want fresh", st.saved.token)
	}
	if st.saved.expiresAt != freshExpires.Unix() {
		t.Errorf("cache exp: got %d, want %d", st.saved.expiresAt, freshExpires.Unix())
	}
}

// TestTokenProvider_AnonymousWhenNoCandidates: no github_app_repos row
// matches the project's repo. Must return zero token, nil error
// (caller goes anonymous). No mint, no verify.
func TestTokenProvider_AnonymousWhenNoCandidates(t *testing.T) {
	t.Parallel()
	st := &fakeTokenStore{} // no repos
	m := &fakeMinter{}
	p := NewDBTokenProvider(st, m, time.Now)

	tok, err := p.GetInstallationToken(context.Background(),
		store.Project{GithubRepo: "public/repo"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "" {
		t.Errorf("token: got %q, want empty (anonymous signal)", tok.Token)
	}
	if m.mintCalls != 0 {
		t.Errorf("mint called %d times, want 0", m.mintCalls)
	}
	if m.listCalls != 0 {
		t.Errorf("verify called %d times, want 0", m.listCalls)
	}
}

// TestTokenProvider_VerifyFailsFallsBackToAnonymous: cached token mints
// fine but the installation no longer has the repo (verify returns a
// list that doesn't include it). With only one candidate, must return
// zero token (anonymous fallback) — never the unverified token.
func TestTokenProvider_VerifyFailsFallsBackToAnonymous(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	expires := now.Add(time.Hour)
	st := &fakeTokenStore{
		repos: []store.GithubAppRepo{{InstallationID: 1, FullName: "h/api"}},
		installations: map[int64]*store.GithubAppInstallation{
			1: {
				ID: 1, AppID: 1, InstallationID: 999,
				AccessToken:          sql.NullString{String: "cached", Valid: true},
				AccessTokenExpiresAt: sql.NullInt64{Int64: expires.Unix(), Valid: true},
			},
		},
	}
	m := &fakeMinter{
		listRepos: map[string][]github.Repository{
			"cached": {{FullName: "h/something-else"}}, // h/api missing
		},
	}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	tok, err := p.GetInstallationToken(context.Background(),
		store.Project{GithubRepo: "h/api"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "" {
		t.Errorf("token: got %q, want empty (anonymous fallback)", tok.Token)
	}
}

// TestTokenProvider_TriesNextCandidate: two installations both claim
// the repo; the first's verify says "no", the second's says "yes".
// Resolver must skip past the first and return the second's token.
func TestTokenProvider_TriesNextCandidate(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	expires := now.Add(time.Hour)
	st := &fakeTokenStore{
		repos: []store.GithubAppRepo{
			{InstallationID: 1, FullName: "h/api"},
			{InstallationID: 2, FullName: "h/api"},
		},
		installations: map[int64]*store.GithubAppInstallation{
			1: {
				ID: 1, AppID: 1, InstallationID: 999,
				AccessToken:          sql.NullString{String: "first", Valid: true},
				AccessTokenExpiresAt: sql.NullInt64{Int64: expires.Unix(), Valid: true},
			},
			2: {
				ID: 2, AppID: 1, InstallationID: 1000,
				AccessToken:          sql.NullString{String: "second", Valid: true},
				AccessTokenExpiresAt: sql.NullInt64{Int64: expires.Unix(), Valid: true},
			},
		},
	}
	m := &fakeMinter{
		listRepos: map[string][]github.Repository{
			"first":  {{FullName: "different/repo"}}, // verify fails
			"second": {{FullName: "h/api"}},          // verify ok
		},
	}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	tok, err := p.GetInstallationToken(context.Background(),
		store.Project{GithubRepo: "h/api"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "second" {
		t.Errorf("token: got %q, want second", tok.Token)
	}
}

// TestTokenProvider_MintFailureSkipsCandidate: a candidate whose mint
// fails (e.g. GitHub returned 404 — installation revoked) is skipped,
// and the resolver continues to the next.
func TestTokenProvider_MintFailureSkipsCandidate(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	st := &fakeTokenStore{
		repos: []store.GithubAppRepo{{InstallationID: 1, FullName: "h/api"}},
		apps: map[int64]*store.GithubApp{
			1: {ID: 1, AppID: 1, PrivateKey: generateRSA(t)},
		},
		installations: map[int64]*store.GithubAppInstallation{
			1: {ID: 1, AppID: 1, InstallationID: 999}, // no cache → must mint
		},
	}
	m := &fakeMinter{mintErr: errors.New("github: status 404")}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	tok, err := p.GetInstallationToken(context.Background(),
		store.Project{GithubRepo: "h/api"})
	if err != nil {
		t.Fatalf("Get: %v", err) // mint failure is not surfaced; we go anonymous
	}
	if tok.Token != "" {
		t.Errorf("token: got %q, want empty (anonymous after mint failure)", tok.Token)
	}
}

// TestTokenProvider_RejectsProjectWithoutRepo: a project row missing a
// github_repo is a programmer error, not a runtime fallback case.
func TestTokenProvider_RejectsProjectWithoutRepo(t *testing.T) {
	t.Parallel()
	p := NewDBTokenProvider(&fakeTokenStore{}, &fakeMinter{}, time.Now)
	_, err := p.GetInstallationToken(context.Background(), store.Project{})
	if err == nil {
		t.Error("want error when project has no github repo")
	}
}
