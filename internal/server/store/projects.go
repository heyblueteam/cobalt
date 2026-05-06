package store

import (
	"context"
	"database/sql"
	"errors"
)

// Project is a row from the projects table.
type Project struct {
	ID                       int64
	Name                     string
	GithubRepo               string
	Branch                   string
	GithubAppInstallationID  sql.NullInt64
	CreatedAt                int64
	UpdatedAt                int64
}

// ErrNotFound is returned by Get* methods when no row matches.
var ErrNotFound = errors.New("store: not found")

// ListProjects returns every project, ordered by name.
func (db *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT id, name, github_repo, branch, github_app_installation_id,
               created_at, updated_at
        FROM projects
        ORDER BY name
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(
			&p.ID, &p.Name, &p.GithubRepo, &p.Branch, &p.GithubAppInstallationID,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProjectByID returns the project with the given id. Returns
// ErrNotFound if no row matches.
func (db *DB) GetProjectByID(ctx context.Context, id int64) (*Project, error) {
	var p Project
	err := db.QueryRowContext(ctx, `
        SELECT id, name, github_repo, branch, github_app_installation_id,
               created_at, updated_at
        FROM projects WHERE id = ?
    `, id).Scan(
		&p.ID, &p.Name, &p.GithubRepo, &p.Branch, &p.GithubAppInstallationID,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectByName returns the project with the given display name.
// Returns ErrNotFound if no row matches.
func (db *DB) GetProjectByName(ctx context.Context, name string) (*Project, error) {
	var p Project
	err := db.QueryRowContext(ctx, `
        SELECT id, name, github_repo, branch, github_app_installation_id,
               created_at, updated_at
        FROM projects WHERE name = ?
    `, name).Scan(
		&p.ID, &p.Name, &p.GithubRepo, &p.Branch, &p.GithubAppInstallationID,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateProject inserts a new project row and returns the row's id.
func (db *DB) CreateProject(ctx context.Context, p Project) (int64, error) {
	res, err := db.ExecContext(ctx, `
        INSERT INTO projects (name, github_repo, branch, github_app_installation_id, created_at, updated_at)
        VALUES (?, ?, ?, ?, unixepoch(), unixepoch())
    `, p.Name, p.GithubRepo, p.Branch, p.GithubAppInstallationID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RenameProject updates a project's display name. Caller is responsible
// for moving any project-name-keyed filesystem state — see
// docs/architecture.md "Identity vs display".
func (db *DB) RenameProject(ctx context.Context, id int64, newName string) error {
	res, err := db.ExecContext(ctx, `
        UPDATE projects SET name = ?, updated_at = unixepoch() WHERE id = ?
    `, newName, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProject removes a project. Foreign keys with ON DELETE CASCADE
// (deployments, env_vars, domains, command_runs) clean up automatically.
func (db *DB) DeleteProject(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
