package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

// EnvVar is a single project environment variable. Value is plain text in
// v1; AES-GCM at rest is deferred per the plan.
type EnvVar struct {
	Key   string
	Value string
}

// ListEnvVars returns every env var for a project, ordered by key.
func (db *DB) ListEnvVars(ctx context.Context, projectID int64) ([]EnvVar, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT key, value FROM env_vars WHERE project_id = ? ORDER BY key`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var e EnvVar
		var v []byte
		if err := rows.Scan(&e.Key, &v); err != nil {
			return nil, err
		}
		e.Value = string(v)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnvVarMap is a convenience wrapper returning env vars as map[string]string,
// the shape docker.BuildOpts.EnvSecrets and docker.RunOpts.EnvVars want.
func (db *DB) EnvVarMap(ctx context.Context, projectID int64) (map[string]string, error) {
	vars, err := db.ListEnvVars(ctx, projectID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Key] = v.Value
	}
	return m, nil
}

// GetEnvVar returns a single env var. Returns ErrNotFound if no row matches.
func (db *DB) GetEnvVar(ctx context.Context, projectID int64, key string) (*EnvVar, error) {
	var v []byte
	err := db.QueryRowContext(ctx,
		`SELECT value FROM env_vars WHERE project_id = ? AND key = ?`,
		projectID, key,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &EnvVar{Key: key, Value: string(v)}, nil
}

// SetEnvVar inserts or updates an env var for a project. Idempotent.
// AES-GCM encryption at rest is deferred per the plan; value stored
// plaintext in v1.
func (db *DB) SetEnvVar(ctx context.Context, projectID int64, key, value string) error {
	if err := validator.ValidateEnvKey(key); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
        INSERT INTO env_vars (project_id, key, value, created_at, updated_at)
        VALUES (?, ?, ?, unixepoch(), unixepoch())
        ON CONFLICT(project_id, key) DO UPDATE SET
            value = excluded.value,
            updated_at = unixepoch()
    `, projectID, key, []byte(value))
	return err
}

// SetEnvVars sets multiple env vars in a single transaction. Either all
// succeed or none do.
func (db *DB) SetEnvVars(ctx context.Context, projectID int64, vars map[string]string) error {
	if len(vars) == 0 {
		return nil
	}
	for k := range vars {
		if err := validator.ValidateEnvKey(k); err != nil {
			return err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
        INSERT INTO env_vars (project_id, key, value, created_at, updated_at)
        VALUES (?, ?, ?, unixepoch(), unixepoch())
        ON CONFLICT(project_id, key) DO UPDATE SET
            value = excluded.value,
            updated_at = unixepoch()
    `)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, v := range vars {
		if _, err := stmt.ExecContext(ctx, projectID, k, []byte(v)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteEnvVar removes a single env var. Returns ErrNotFound if no row
// matched.
func (db *DB) DeleteEnvVar(ctx context.Context, projectID int64, key string) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM env_vars WHERE project_id = ? AND key = ?`,
		projectID, key,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
