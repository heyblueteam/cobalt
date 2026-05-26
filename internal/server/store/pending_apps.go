package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

type PendingApp struct {
	ID           int64
	State        string
	Organization string
	CreatedAt    int64
	ExpiresAt    int64
}

func (db *DB) CreatePendingApp(ctx context.Context, organization string, expiresAtUnix int64) (id int64, state string, err error) {
	state, err = randomHex(16)
	if err != nil {
		return 0, "", fmt.Errorf("store: pending app state: %w", err)
	}
	resp, err := db.ExecuteSingle(
		ctx,
		`INSERT INTO pending_github_apps (state, organization, created_at, expires_at)
		 VALUES (?, ?, strftime('%s', 'now'), ?)`,
		state, organization, expiresAtUnix,
	)
	if err != nil {
		return 0, "", err
	}
	if len(resp.Results) == 0 {
		return 0, state, nil
	}
	id = resp.Results[0].LastInsertID
	return id, state, nil
}

func (db *DB) GetPendingApp(ctx context.Context, id int64) (*PendingApp, error) {
	resp, err := db.QuerySingle(
		ctx,
		`SELECT id, state, organization, created_at, expires_at
		 FROM pending_github_apps WHERE id = ?`,
		id,
	)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	row := results[0].Values[0]
	return &PendingApp{
		ID:           toInt64(row[0]),
		State:        toString(row[1]),
		Organization: toString(row[2]),
		CreatedAt:    toInt64(row[3]),
		ExpiresAt:    toInt64(row[4]),
	}, nil
}

func (db *DB) DeletePendingApp(ctx context.Context, id int64) error {
	resp, err := db.ExecuteSingle(
		ctx,
		`DELETE FROM pending_github_apps WHERE id = ?`,
		id,
	)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) DeleteExpiredPendingApps(ctx context.Context, nowUnix int64) (int64, error) {
	resp, err := db.ExecuteSingle(
		ctx,
		`DELETE FROM pending_github_apps WHERE expires_at <= ? AND expires_at > 0`,
		nowUnix,
	)
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].RowsAffected, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
