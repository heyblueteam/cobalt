package store

import "context"

// ListDomainsForProject returns every domain bound to the given project,
// ordered by id (insertion order). Used by the deploy flow's Caddy
// reconcile step.
func (db *DB) ListDomainsForProject(ctx context.Context, projectID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM domains WHERE project_id = ? ORDER BY id`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddDomain inserts a new domain row for a project. Used by the API for
// `cobalt domains add`. Domain names are globally unique (enforced by the
// schema's UNIQUE constraint).
func (db *DB) AddDomain(ctx context.Context, projectID int64, name string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO domains (project_id, name, created_at) VALUES (?, ?, unixepoch())`,
		projectID, name,
	)
	return err
}

// RemoveDomain deletes a domain by name. Returns ErrNotFound if no row
// matches.
func (db *DB) RemoveDomain(ctx context.Context, projectID int64, name string) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM domains WHERE project_id = ? AND name = ?`,
		projectID, name,
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
