package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

type EnvVar struct {
	Key       string
	Value     string
	UpdatedAt int64 // unix seconds; surfaced via the API so clients can
	// detect staleness (env updated since last successful deploy =
	// running containers haven't picked it up yet).
}

func (db *DB) ListEnvVars(ctx context.Context, projectID int64) ([]EnvVar, error) {
	stmt, err := rqlitehttp.NewSQLStatement(`
		SELECT key, value, updated_at FROM env_vars WHERE project_id = ? ORDER BY key
	`, projectID)
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
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
	out := make([]EnvVar, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		stored := blobToString(row[1])
		val, err := db.decryptValue(stored)
		if err != nil {
			return nil, err
		}
		out = append(out, EnvVar{
			Key:       toString(row[0]),
			Value:     val,
			UpdatedAt: toInt64(row[2]),
		})
	}
	return out, nil
}

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

func (db *DB) GetEnvVar(ctx context.Context, projectID int64, key string) (*EnvVar, error) {
	resp, err := db.QuerySingle(ctx, `
		SELECT value FROM env_vars WHERE project_id = ? AND key = ?
	`, projectID, key)
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
	val, err := db.decryptValue(blobToString(results[0].Values[0][0]))
	if err != nil {
		return nil, err
	}
	return &EnvVar{Key: key, Value: val}, nil
}

func (db *DB) SetEnvVar(ctx context.Context, projectID int64, key, value string) error {
	if err := validator.ValidateEnvKey(key); err != nil {
		return err
	}
	stored, err := db.encryptValue(value)
	if err != nil {
		return err
	}
	_, err = db.ExecuteSingle(ctx, `
		INSERT INTO env_vars (project_id, key, value, created_at, updated_at)
		VALUES (?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
		ON CONFLICT(project_id, key) DO UPDATE SET
			value = excluded.value,
			updated_at = strftime('%s', 'now')
	`, projectID, key, stored)
	return err
}

func (db *DB) SetEnvVars(ctx context.Context, projectID int64, vars map[string]string) error {
	if len(vars) == 0 {
		return nil
	}
	for k := range vars {
		if err := validator.ValidateEnvKey(k); err != nil {
			return err
		}
	}
	stmts := make(rqlitehttp.SQLStatements, 0, len(vars))
	for k, v := range vars {
		stored, err := db.encryptValue(v)
		if err != nil {
			return err
		}
		stmt, err := rqlitehttp.NewSQLStatement(`
			INSERT INTO env_vars (project_id, key, value, created_at, updated_at)
			VALUES (?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
			ON CONFLICT(project_id, key) DO UPDATE SET
				value = excluded.value,
				updated_at = strftime('%s', 'now')
		`, projectID, k, stored)
		if err != nil {
			return err
		}
		stmts = append(stmts, stmt)
	}
	resp, err := db.Execute(ctx, stmts, &rqlitehttp.ExecuteOptions{Transaction: true})
	if err != nil {
		return err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return errors.New(errMsg)
	}
	return nil
}

// MigrateEnvVarsToEncrypted scans env_vars and rewrites any rows whose
// value isn't already a v1 ciphertext frame. Idempotent: safe to call
// on every daemon boot. Returns the number of rows it encrypted.
//
// Requires a configured cipher; with no cipher wired this is a no-op.
func (db *DB) MigrateEnvVarsToEncrypted(ctx context.Context) (int, error) {
	if db.cipher == nil {
		return 0, nil
	}
	resp, err := db.QuerySingle(ctx,
		`SELECT project_id, key, value FROM env_vars`)
	if err != nil {
		return 0, fmt.Errorf("env migration: select: %w", err)
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
		stored := blobToString(row[2])
		if encryption.IsCiphertext(stored) {
			continue
		}
		ct, err := db.cipher.Encrypt([]byte(stored))
		if err != nil {
			return 0, fmt.Errorf("env migration: encrypt: %w", err)
		}
		stmt, err := rqlitehttp.NewSQLStatement(
			`UPDATE env_vars SET value = ? WHERE project_id = ? AND key = ?`,
			ct, toInt64(row[0]), toString(row[1]),
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
		return 0, fmt.Errorf("env migration: update: %w", err)
	}
	return len(stmts), nil
}

func (db *DB) DeleteEnvVar(ctx context.Context, projectID int64, key string) error {
	resp, err := db.ExecuteSingle(ctx, `
		DELETE FROM env_vars WHERE project_id = ? AND key = ?
	`, projectID, key)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func blobToString(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return toString(v)
}
