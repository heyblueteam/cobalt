package store

import (
	"context"
	"errors"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

type EnvVar struct {
	Key   string
	Value string
}

func (db *DB) ListEnvVars(ctx context.Context, projectID int64) ([]EnvVar, error) {
	stmt, err := rqlitehttp.NewSQLStatement(`
		SELECT key, value FROM env_vars WHERE project_id = ? ORDER BY key
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
		var e EnvVar
		e.Key = toString(row[0])
		e.Value = blobToString(row[1])
		out = append(out, e)
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
	return &EnvVar{Key: key, Value: blobToString(results[0].Values[0][0])}, nil
}

func (db *DB) SetEnvVar(ctx context.Context, projectID int64, key, value string) error {
	if err := validator.ValidateEnvKey(key); err != nil {
		return err
	}
	_, err := db.ExecuteSingle(ctx, `
		INSERT INTO env_vars (project_id, key, value, created_at, updated_at)
		VALUES (?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
		ON CONFLICT(project_id, key) DO UPDATE SET
			value = excluded.value,
			updated_at = strftime('%s', 'now')
	`, projectID, key, value)
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
		stmt, err := rqlitehttp.NewSQLStatement(`
			INSERT INTO env_vars (project_id, key, value, created_at, updated_at)
			VALUES (?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
			ON CONFLICT(project_id, key) DO UPDATE SET
				value = excluded.value,
				updated_at = strftime('%s', 'now')
		`, projectID, k, v)
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
