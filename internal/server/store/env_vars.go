package store

import "context"

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
