package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

type GithubApp struct {
	ID            int64
	AppID         int64
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

type GithubAppInstallation struct {
	ID                   int64
	AppID                int64
	InstallationID       int64
	AccountLogin         string
	AccessToken          sql.NullString
	AccessTokenExpiresAt sql.NullInt64
	CreatedAt            int64
}

// scanGithubApp builds a GithubApp from a result row, decrypting
// private_key and webhook_secret on the way out.
func (db *DB) scanGithubApp(row []any) (GithubApp, error) {
	pem, err := db.decryptValue(toString(row[4]))
	if err != nil {
		return GithubApp{}, fmt.Errorf("github_apps.private_key decrypt: %w", err)
	}
	whSecret, err := db.decryptValue(toString(row[5]))
	if err != nil {
		return GithubApp{}, fmt.Errorf("github_apps.webhook_secret decrypt: %w", err)
	}
	a := GithubApp{
		ID:            toInt64(row[0]),
		AppID:         toInt64(row[1]),
		Owner:         toString(row[3]),
		PrivateKey:    pem,
		WebhookSecret: whSecret,
		CreatedAt:     toInt64(row[10]),
	}
	if row[2] != nil {
		a.Slug = sql.NullString{String: toString(row[2]), Valid: true}
	}
	if row[6] != nil {
		a.ClientID = sql.NullString{String: toString(row[6]), Valid: true}
	}
	if row[7] != nil {
		a.ClientSecret = sql.NullString{String: toString(row[7]), Valid: true}
	}
	if row[8] != nil {
		a.Name = sql.NullString{String: toString(row[8]), Valid: true}
	}
	if row[9] != nil {
		a.HTMLURL = sql.NullString{String: toString(row[9]), Valid: true}
	}
	return a, nil
}

func (db *DB) GetGithubApp(ctx context.Context, id int64) (*GithubApp, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, app_id, slug, owner, private_key, webhook_secret,
               client_id, client_secret, name, html_url, created_at
        FROM github_apps WHERE id = ?
    `, id)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	a, err := db.scanGithubApp(results[0].Values[0])
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (db *DB) GetGithubAppInstallation(ctx context.Context, id int64) (*GithubAppInstallation, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, app_id, installation_id, account_login,
               access_token, access_token_expires_at, created_at
        FROM github_app_installations WHERE id = ?
    `, id)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	row := results[0].Values[0]
	inst := GithubAppInstallation{
		ID:             toInt64(row[0]),
		AppID:          toInt64(row[1]),
		InstallationID: toInt64(row[2]),
		AccountLogin:   toString(row[3]),
		CreatedAt:      toInt64(row[6]),
	}
	if row[4] != nil {
		inst.AccessToken = sql.NullString{String: toString(row[4]), Valid: true}
	}
	if row[5] != nil {
		inst.AccessTokenExpiresAt = sql.NullInt64{Int64: toInt64(row[5]), Valid: true}
	}
	return &inst, nil
}

func (db *DB) SetInstallationToken(ctx context.Context, id int64, token string, expiresAtUnix int64) error {
	resp, err := db.ExecuteSingle(ctx, `
        UPDATE github_app_installations
        SET access_token = ?, access_token_expires_at = ?
        WHERE id = ?
    `, token, expiresAtUnix, id)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) CreateGithubApp(ctx context.Context, a GithubApp) (int64, error) {
	pem, err := db.encryptValue(a.PrivateKey)
	if err != nil {
		return 0, fmt.Errorf("github_apps.private_key encrypt: %w", err)
	}
	whSecret, err := db.encryptValue(a.WebhookSecret)
	if err != nil {
		return 0, fmt.Errorf("github_apps.webhook_secret encrypt: %w", err)
	}
	resp, err := db.ExecuteSingle(ctx, `
        INSERT INTO github_apps (
            app_id, slug, owner, private_key, webhook_secret,
            client_id, client_secret, name, html_url, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
    `, a.AppID, nullStringArg(a.Slug), a.Owner, pem, whSecret,
		nullStringArg(a.ClientID), nullStringArg(a.ClientSecret),
		nullStringArg(a.Name), nullStringArg(a.HTMLURL))
	if err != nil {
		return 0, err
	}
	if err := resultErr(resp); err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].LastInsertID, nil
}

// nullStringArg converts a sql.NullString to a value the rqlite-go-http
// SDK binds correctly. The SDK marshals struct values verbatim, which
// breaks parameter ordering when an INVALID NullString is passed; we
// must convert to nil (= bind NULL) instead.
func nullStringArg(n sql.NullString) any {
	if !n.Valid {
		return nil
	}
	return n.String
}

// resultErr surfaces a per-statement error. rqlite returns HTTP 200 with
// the SQL error embedded in Results[0].Error — silent failures otherwise.
func resultErr(resp *rqlitehttp.ExecuteResponse) error {
	if resp == nil {
		return nil
	}
	for _, r := range resp.Results {
		if r.Error != "" {
			return errors.New(r.Error)
		}
	}
	return nil
}

func (db *DB) CreateGithubAppInstallation(ctx context.Context, inst GithubAppInstallation) (int64, error) {
	resp, err := db.ExecuteSingle(ctx, `
        INSERT INTO github_app_installations (
            app_id, installation_id, account_login, created_at
        ) VALUES (?, ?, ?, strftime('%s', 'now'))
    `, inst.AppID, inst.InstallationID, inst.AccountLogin)
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].LastInsertID, nil
}

func (db *DB) GetGithubAppByAppID(ctx context.Context, appID int64) (*GithubApp, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, app_id, slug, owner, private_key, webhook_secret,
               client_id, client_secret, name, html_url, created_at
        FROM github_apps WHERE app_id = ?
    `, appID)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	a, err := db.scanGithubApp(results[0].Values[0])
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (db *DB) GetGithubAppInstallationByInstallationID(ctx context.Context, instID int64) (*GithubAppInstallation, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, app_id, installation_id, account_login,
               access_token, access_token_expires_at, created_at
        FROM github_app_installations WHERE installation_id = ?
    `, instID)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	row := results[0].Values[0]
	inst := GithubAppInstallation{
		ID:             toInt64(row[0]),
		AppID:          toInt64(row[1]),
		InstallationID: toInt64(row[2]),
		AccountLogin:   toString(row[3]),
		CreatedAt:      toInt64(row[6]),
	}
	if row[4] != nil {
		inst.AccessToken = sql.NullString{String: toString(row[4]), Valid: true}
	}
	if row[5] != nil {
		inst.AccessTokenExpiresAt = sql.NullInt64{Int64: toInt64(row[5]), Valid: true}
	}
	return &inst, nil
}

func (db *DB) DeleteGithubAppInstallation(ctx context.Context, id int64) error {
	resp, err := db.ExecuteSingle(ctx, `DELETE FROM github_app_installations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) DeleteGithubApp(ctx context.Context, id int64) error {
	resp, err := db.ExecuteSingle(ctx, `DELETE FROM github_apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MigrateGithubAppsToEncrypted scans github_apps and rewrites any
// rows whose private_key or webhook_secret isn't already a v1
// ciphertext frame. Idempotent: safe to call on every daemon boot.
// Returns the number of rows it touched (each row counts once even
// if both columns needed encrypting).
func (db *DB) MigrateGithubAppsToEncrypted(ctx context.Context) (int, error) {
	c := db.cipherSnapshot()
	if c == nil {
		return 0, nil
	}
	resp, err := db.QuerySingle(ctx,
		`SELECT id, private_key, webhook_secret FROM github_apps`)
	if err != nil {
		return 0, fmt.Errorf("github_apps migration: select: %w", err)
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return 0, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return 0, nil
	}
	var stmts rqlitehttp.SQLStatements
	for _, row := range results[0].Values {
		id := toInt64(row[0])
		pem := toString(row[1])
		whSecret := toString(row[2])
		needsPEM := !encryption.IsCiphertext(pem)
		needsSecret := !encryption.IsCiphertext(whSecret)
		if !needsPEM && !needsSecret {
			continue
		}
		if needsPEM {
			ct, err := c.Encrypt([]byte(pem))
			if err != nil {
				return 0, fmt.Errorf("github_apps migration: encrypt pem: %w", err)
			}
			pem = ct
		}
		if needsSecret {
			ct, err := c.Encrypt([]byte(whSecret))
			if err != nil {
				return 0, fmt.Errorf("github_apps migration: encrypt webhook_secret: %w", err)
			}
			whSecret = ct
		}
		stmt, err := rqlitehttp.NewSQLStatement(
			`UPDATE github_apps SET private_key = ?, webhook_secret = ? WHERE id = ?`,
			pem, whSecret, id,
		)
		if err != nil {
			return 0, err
		}
		stmts = append(stmts, stmt)
	}
	if len(stmts) == 0 {
		return 0, nil
	}
	if _, err := db.Execute(ctx, stmts, &rqlitehttp.ExecuteOptions{Transaction: true}); err != nil {
		return 0, fmt.Errorf("github_apps migration: update: %w", err)
	}
	return len(stmts), nil
}

func (db *DB) ListGithubApps(ctx context.Context) ([]GithubApp, error) {
	stmt, err := rqlitehttp.NewSQLStatement(`
        SELECT id, app_id, slug, owner, private_key, webhook_secret,
               client_id, client_secret, name, html_url, created_at
        FROM github_apps ORDER BY id
    `)
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return nil, nil
	}
	var out []GithubApp
	for _, row := range results[0].Values {
		a, err := db.scanGithubApp(row)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (db *DB) ListGithubAppInstallations(ctx context.Context, appID int64) ([]GithubAppInstallation, error) {
	stmt, err := rqlitehttp.NewSQLStatement(`
        SELECT id, app_id, installation_id, account_login,
               access_token, access_token_expires_at, created_at
        FROM github_app_installations WHERE app_id = ? ORDER BY id
    `, appID)
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return nil, nil
	}
	var out []GithubAppInstallation
	for _, row := range results[0].Values {
		inst := GithubAppInstallation{
			ID:             toInt64(row[0]),
			AppID:          toInt64(row[1]),
			InstallationID: toInt64(row[2]),
			AccountLogin:   toString(row[3]),
			CreatedAt:      toInt64(row[6]),
		}
		if row[4] != nil {
			inst.AccessToken = sql.NullString{String: toString(row[4]), Valid: true}
		}
		if row[5] != nil {
			inst.AccessTokenExpiresAt = sql.NullInt64{Int64: toInt64(row[5]), Valid: true}
		}
		out = append(out, inst)
	}
	return out, nil
}
