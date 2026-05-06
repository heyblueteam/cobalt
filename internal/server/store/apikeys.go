package store

import (
	"context"
	"database/sql"
	"errors"
)

// APIKey is a row from apikeys. The raw key is never persisted —
// only its sha256 hash. The API key handler returns the raw bytes
// once at creation time and they cannot be recovered after that.
type APIKey struct {
	ID         int64
	Name       string
	CreatedAt  int64
	LastUsedAt sql.NullInt64
}

// CreateAPIKey inserts a new row. The caller is responsible for
// generating the random key bytes and passing the sha256 hash here —
// keeping hashing logic out of store avoids a circular dep with
// internal/server/middleware (which provides HashAPIKey for the
// auth path's lookup).
func (db *DB) CreateAPIKey(ctx context.Context, hash, name string) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO apikeys (key_hash, name, created_at) VALUES (?, ?, unixepoch())`,
		hash, name,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListAPIKeys returns every key row sans hash, ordered by created_at.
func (db *DB) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, created_at, last_used_at FROM apikeys ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetAPIKeyByID fetches a single key by id. Returns ErrNotFound when
// no row matches.
func (db *DB) GetAPIKeyByID(ctx context.Context, id int64) (*APIKey, error) {
	var k APIKey
	err := db.QueryRowContext(ctx,
		`SELECT id, name, created_at, last_used_at FROM apikeys WHERE id = ?`,
		id,
	).Scan(&k.ID, &k.Name, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// DeleteAPIKey revokes a key by id. Returns ErrNotFound when no row
// matched (idempotent failure mode for callers).
func (db *DB) DeleteAPIKey(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM apikeys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
