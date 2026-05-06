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

type fakeTokenStore struct {
	app          *store.GithubApp
	installation *store.GithubAppInstallation
	saved        struct {
		token     string
		expiresAt int64
	}
}

func (f *fakeTokenStore) GetGithubAppInstallation(_ context.Context, _ int64) (*store.GithubAppInstallation, error) {
	if f.installation == nil {
		return nil, store.ErrNotFound
	}
	return f.installation, nil
}
func (f *fakeTokenStore) GetGithubApp(_ context.Context, _ int64) (*store.GithubApp, error) {
	if f.app == nil {
		return nil, store.ErrNotFound
	}
	return f.app, nil
}
func (f *fakeTokenStore) SetInstallationToken(_ context.Context, _ int64, token string, exp int64) error {
	f.saved.token = token
	f.saved.expiresAt = exp
	return nil
}

type fakeMinter struct {
	called int
	tok    github.InstallationToken
	err    error
}

func (m *fakeMinter) MintInstallationToken(_ context.Context, _ string, _ int64) (github.InstallationToken, error) {
	m.called++
	if m.err != nil {
		return github.InstallationToken{}, m.err
	}
	return m.tok, nil
}

func TestTokenProvider_UsesCacheWhenValid(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	expires := now.Add(time.Hour)
	st := &fakeTokenStore{
		installation: &store.GithubAppInstallation{
			ID: 1, AppID: 1, InstallationID: 999,
			AccessToken:          sql.NullString{String: "cached", Valid: true},
			AccessTokenExpiresAt: sql.NullInt64{Int64: expires.Unix(), Valid: true},
		},
	}
	m := &fakeMinter{}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	tok, err := p.GetInstallationToken(context.Background(), store.Project{
		GithubAppInstallationID: sql.NullInt64{Int64: 1, Valid: true},
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "cached" {
		t.Errorf("token: got %q, want cached", tok.Token)
	}
	if m.called != 0 {
		t.Errorf("mint called %d times, want 0", m.called)
	}
}

func TestTokenProvider_MintsWhenCacheStale(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	st := &fakeTokenStore{
		app: &store.GithubApp{
			ID: 1, AppID: 12345, PrivateKey: generateRSA(t),
		},
		installation: &store.GithubAppInstallation{
			ID: 1, AppID: 1, InstallationID: 999,
			// Cached but expires within margin → should refresh.
			AccessToken:          sql.NullString{String: "stale", Valid: true},
			AccessTokenExpiresAt: sql.NullInt64{Int64: now.Add(time.Minute).Unix(), Valid: true},
		},
	}
	freshExpires := now.Add(time.Hour)
	m := &fakeMinter{tok: github.InstallationToken{Token: "fresh", ExpiresAt: freshExpires}}

	p := NewDBTokenProvider(st, m, func() time.Time { return now })
	tok, err := p.GetInstallationToken(context.Background(), store.Project{
		GithubAppInstallationID: sql.NullInt64{Int64: 1, Valid: true},
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.Token != "fresh" {
		t.Errorf("token: got %q, want fresh", tok.Token)
	}
	if m.called != 1 {
		t.Errorf("mint called %d times, want 1", m.called)
	}
	if st.saved.token != "fresh" {
		t.Errorf("cache write: got %q, want fresh", st.saved.token)
	}
	if st.saved.expiresAt != freshExpires.Unix() {
		t.Errorf("cache exp: got %d, want %d", st.saved.expiresAt, freshExpires.Unix())
	}
}

func TestTokenProvider_RejectsProjectWithoutInstallation(t *testing.T) {
	t.Parallel()
	p := NewDBTokenProvider(&fakeTokenStore{}, &fakeMinter{}, time.Now)
	_, err := p.GetInstallationToken(context.Background(), store.Project{})
	if err == nil {
		t.Error("want error when project has no installation")
	}
}

func TestTokenProvider_PropagatesMintFailure(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000_000, 0)
	st := &fakeTokenStore{
		app: &store.GithubApp{ID: 1, AppID: 1, PrivateKey: generateRSA(t)},
		installation: &store.GithubAppInstallation{
			ID: 1, AppID: 1, InstallationID: 999,
		},
	}
	m := &fakeMinter{err: errors.New("github down")}
	p := NewDBTokenProvider(st, m, func() time.Time { return now })

	_, err := p.GetInstallationToken(context.Background(), store.Project{
		GithubAppInstallationID: sql.NullInt64{Int64: 1, Valid: true},
	})
	if err == nil {
		t.Error("want error when mint fails")
	}
}
