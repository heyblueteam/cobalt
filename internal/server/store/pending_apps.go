package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// PendingApp is a row from pending_github_apps — a partial GitHub App
// registration in flight. The state token is the CSRF guard verified on
// callback.
type PendingApp struct {
	ID           int64
	State        string
	Organization string
	CreatedAt    int64
	ExpiresAt    int64
}

// CreatePendingApp inserts a new row with a freshly-generated state
// token (32-char hex). Returns the row id and the state for inclusion
// in the manifest form.
func (db *DB) CreatePendingApp(ctx context.Context, organization string, expiresAtUnix int64) (id int64, state string, err error) {
	state, err = randomHex(16)
	if err != nil {
		return 0, "", fmt.Errorf("store: pending app state: %w", err)
	}
	res, err := db.ExecContext(ctx, `
        INSERT INTO pending_github_apps (state, organization, created_at, expires_at)
        VALUES (?, ?, unixepoch(), ?)
    `, state, organization, expiresAtUnix)
	if err != nil {
		return 0, "", err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, "", err
	}
	return id, state, nil
}

// GetPendingApp looks up a pending row by id. Callers must verify both
// that the row exists AND that the supplied state token matches before
// trusting the row.
func (db *DB) GetPendingApp(ctx context.Context, id int64) (*PendingApp, error) {
	var p PendingApp
	err := db.QueryRowContext(ctx, `
        SELECT id, state, organization, created_at, expires_at
        FROM pending_github_apps WHERE id = ?
    `, id).Scan(&p.ID, &p.State, &p.Organization, &p.CreatedAt, &p.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePendingApp removes a row. Used both on successful manifest flow
// completion and to revoke an unused row.
func (db *DB) DeletePendingApp(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM pending_github_apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpiredPendingApps removes pending_github_apps rows whose
// expires_at is at or before nowUnix. Returns how many rows were dropped.
//
// Called periodically by the worker. Stale rows accumulate when users
// abandon the manifest flow halfway through.
func (db *DB) DeleteExpiredPendingApps(ctx context.Context, nowUnix int64) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM pending_github_apps WHERE expires_at <= ? AND expires_at > 0`,
		nowUnix,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// randomHex returns a 2*n-char hex string from the system CSPRNG.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
