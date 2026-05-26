package store

import (
	"context"
	"errors"
)

type APIKey struct {
	ID         int64
	Name       string
	CreatedAt  int64
	LastUsedAt int64
}

func (db *DB) CreateAPIKey(ctx context.Context, hash, name string) (int64, error) {
	resp, err := db.ExecuteSingle(
		ctx,
		`INSERT INTO apikeys (key_hash, name, created_at) VALUES (?, ?, strftime('%s', 'now'))`,
		hash, name,
	)
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].LastInsertID, nil
}

func (db *DB) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	resp, err := db.QuerySingle(
		ctx,
		`SELECT id, name, created_at, last_used_at FROM apikeys ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return nil, nil
	}
	out := make([]APIKey, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		var k APIKey
		k.ID = toInt64(row[0])
		k.Name = toString(row[1])
		k.CreatedAt = toInt64(row[2])
		k.LastUsedAt = toInt64(row[3])
		out = append(out, k)
	}
	return out, nil
}

func (db *DB) GetAPIKeyByID(ctx context.Context, id int64) (*APIKey, error) {
	resp, err := db.QuerySingle(
		ctx,
		`SELECT id, name, created_at, last_used_at FROM apikeys WHERE id = ?`,
		id,
	)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	row := results[0].Values[0]
	var k APIKey
	k.ID = toInt64(row[0])
	k.Name = toString(row[1])
	k.CreatedAt = toInt64(row[2])
	k.LastUsedAt = toInt64(row[3])
	return &k, nil
}

func (db *DB) DeleteAPIKey(ctx context.Context, id int64) error {
	resp, err := db.ExecuteSingle(ctx, `DELETE FROM apikeys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
