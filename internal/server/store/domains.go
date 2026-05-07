package store

import (
	"context"
)

func (db *DB) ListDomainsForProject(ctx context.Context, projectID int64) ([]string, error) {
	sql := `SELECT name FROM domains WHERE project_id = ? ORDER BY id`
	resp, err := db.QuerySingle(ctx, sql, projectID)
	if err != nil {
		return nil, err
	}

	var out []string
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return out, nil
	}
	for _, row := range results[0].Values {
		if len(row) > 0 {
			out = append(out, toString(row[0]))
		}
	}
	return out, nil
}

func (db *DB) AddDomain(ctx context.Context, projectID int64, name string) error {
	sql := `INSERT INTO domains (project_id, name, created_at) VALUES (?, ?, strftime('%s', 'now'))`
	_, err := db.ExecuteSingle(ctx, sql, projectID, name)
	return err
}

func (db *DB) RemoveDomain(ctx context.Context, projectID int64, name string) error {
	sql := `DELETE FROM domains WHERE project_id = ? AND name = ?`
	resp, err := db.ExecuteSingle(ctx, sql, projectID, name)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
