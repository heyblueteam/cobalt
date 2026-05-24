package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

var _ *sql.DB = nil // enforce that we don't accidentally use database/sql

// Project is a row from the projects table.
//
// Path is an optional sub-path inside the repo where the project's
// cobalt.json + Dockerfile contexts live. Empty means repo root, which
// is the original (and most common) shape. A non-empty Path lets one
// repo host multiple deployments — useful for monorepos where, e.g.,
// `api/` and `app-next/` ship as separate cobalt projects from a
// single GitHub repo. See `pkg/cobaltapi/validator.ValidateProjectPath`
// for the shape rules.
type Project struct {
	ID                      int64
	Name                    string
	GithubRepo              string
	Branch                  string
	Path                    string
	GithubAppInstallationID sql.NullInt64
	CreatedAt               int64
	UpdatedAt               int64
}

// ListProjects returns every project, ordered by name.
func (db *DB) ListProjects(ctx context.Context) ([]Project, error) {
	stmt, err := rqlitehttp.NewSQLStatement(`
        SELECT id, name, github_repo, branch, path,
               github_app_installation_id, created_at, updated_at
        FROM projects
        ORDER BY name
    `)
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
	out := make([]Project, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		out = append(out, scanProjectRow(row))
	}
	return out, nil
}

// GetProjectByID returns the project with the given id. Returns
// ErrNotFound if no row matches.
func (db *DB) GetProjectByID(ctx context.Context, id int64) (*Project, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, name, github_repo, branch, path,
               github_app_installation_id, created_at, updated_at
        FROM projects WHERE id = ?
    `, id)
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
	p := scanProjectRow(results[0].Values[0])
	return &p, nil
}

// GetProjectByName returns the project with the given display name.
// Returns ErrNotFound if no row matches.
func (db *DB) GetProjectByName(ctx context.Context, name string) (*Project, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, name, github_repo, branch, path,
               github_app_installation_id, created_at, updated_at
        FROM projects WHERE name = ?
    `, name)
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
	p := scanProjectRow(results[0].Values[0])
	return &p, nil
}

// CreateProject inserts a new project row and returns the row's id.
func (db *DB) CreateProject(ctx context.Context, p Project) (int64, error) {
	if err := validator.ValidateProjectName(p.Name); err != nil {
		return 0, err
	}
	if err := validator.ValidateProjectPath(p.Path); err != nil {
		return 0, err
	}
	resp, err := db.ExecuteSingle(ctx, `
        INSERT INTO projects (name, github_repo, branch, path, github_app_installation_id, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
    `, p.Name, p.GithubRepo, p.Branch, p.Path, p.GithubAppInstallationID)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, ErrProjectNameTaken
		}
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	if resp.Results[0].Error != "" {
		if isUniqueConstraintErr(errors.New(resp.Results[0].Error)) {
			return 0, ErrProjectNameTaken
		}
		return 0, errors.New(resp.Results[0].Error)
	}
	return resp.Results[0].LastInsertID, nil
}

// scanProjectRow centralizes column-order knowledge so the four
// queries that SELECT projects stay in sync when columns are added.
// Column order MUST match the SELECT in every caller above.
func scanProjectRow(row []any) Project {
	var p Project
	p.ID = toInt64(row[0])
	p.Name = toString(row[1])
	p.GithubRepo = toString(row[2])
	p.Branch = toString(row[3])
	p.Path = toString(row[4])
	if row[5] != nil {
		p.GithubAppInstallationID = sql.NullInt64{Int64: toInt64(row[5]), Valid: true}
	}
	p.CreatedAt = toInt64(row[6])
	p.UpdatedAt = toInt64(row[7])
	return p
}

// RenameProject updates a project's display name. Caller is responsible
// for moving any project-name-keyed filesystem state — see
// docs/architecture.md "Identity vs display".
func (db *DB) RenameProject(ctx context.Context, id int64, newName string) error {
	if err := validator.ValidateProjectName(newName); err != nil {
		return err
	}
	resp, err := db.ExecuteSingle(ctx, `
        UPDATE projects SET name = ?, updated_at = strftime('%s', 'now') WHERE id = ?
    `, newName, id)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProject removes a project. Foreign keys with ON DELETE CASCADE
// (deployments, env_vars, domains, command_runs) clean up automatically.
func (db *DB) DeleteProject(ctx context.Context, id int64) error {
	resp, err := db.ExecuteSingle(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	return true // rqlite returns errors with constraint info
}

func toInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		i, _ := x.Int64()
		return i
	}
	return 0
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
