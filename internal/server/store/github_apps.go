package store

import (
	"context"
	"database/sql"
	"errors"
)

// GithubApp is a row from github_apps. The PrivateKey is the PEM-encoded
// RSA private key cobalt uses to mint installation tokens.
type GithubApp struct {
	ID            int64
	AppID         int64 // GitHub's id
	Slug          sql.NullString
	Owner         string
	PrivateKey    string
	WebhookSecret string
	ClientID      sql.NullString
	ClientSecret  sql.NullString
	Name          sql.NullString
	HTMLURL       sql.NullString
	CreatedAt     int64
}

// GithubAppInstallation is a row from github_app_installations.
type GithubAppInstallation struct {
	ID                   int64
	AppID                int64 // FK to github_apps.id
	InstallationID       int64 // GitHub's installation id
	AccountLogin         string
	AccessToken          sql.NullString
	AccessTokenExpiresAt sql.NullInt64
	CreatedAt            int64
}

// GetGithubApp returns the app row for a given local id.
func (db *DB) GetGithubApp(ctx context.Context, id int64) (*GithubApp, error) {
	var a GithubApp
	err := db.QueryRowContext(ctx, `
        SELECT id, app_id, slug, owner, private_key, webhook_secret,
               client_id, client_secret, name, html_url, created_at
        FROM github_apps WHERE id = ?
    `, id).Scan(
		&a.ID, &a.AppID, &a.Slug, &a.Owner, &a.PrivateKey, &a.WebhookSecret,
		&a.ClientID, &a.ClientSecret, &a.Name, &a.HTMLURL, &a.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetGithubAppInstallation returns an installation row by local id.
func (db *DB) GetGithubAppInstallation(ctx context.Context, id int64) (*GithubAppInstallation, error) {
	var inst GithubAppInstallation
	err := db.QueryRowContext(ctx, `
        SELECT id, app_id, installation_id, account_login,
               access_token, access_token_expires_at, created_at
        FROM github_app_installations WHERE id = ?
    `, id).Scan(
		&inst.ID, &inst.AppID, &inst.InstallationID, &inst.AccountLogin,
		&inst.AccessToken, &inst.AccessTokenExpiresAt, &inst.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// SetInstallationToken caches a freshly-minted installation token on the
// installation row.
func (db *DB) SetInstallationToken(ctx context.Context, id int64, token string, expiresAtUnix int64) error {
	res, err := db.ExecContext(ctx, `
        UPDATE github_app_installations
        SET access_token = ?, access_token_expires_at = ?
        WHERE id = ?
    `, token, expiresAtUnix, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateGithubApp inserts a row from a manifest-conversion result.
func (db *DB) CreateGithubApp(ctx context.Context, a GithubApp) (int64, error) {
	res, err := db.ExecContext(ctx, `
        INSERT INTO github_apps (
            app_id, slug, owner, private_key, webhook_secret,
            client_id, client_secret, name, html_url, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch())
    `, a.AppID, a.Slug, a.Owner, a.PrivateKey, a.WebhookSecret,
		a.ClientID, a.ClientSecret, a.Name, a.HTMLURL)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateGithubAppInstallation inserts a row from an `installation` webhook.
func (db *DB) CreateGithubAppInstallation(ctx context.Context, inst GithubAppInstallation) (int64, error) {
	res, err := db.ExecContext(ctx, `
        INSERT INTO github_app_installations (
            app_id, installation_id, account_login, created_at
        ) VALUES (?, ?, ?, unixepoch())
    `, inst.AppID, inst.InstallationID, inst.AccountLogin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
