package store

import (
	"context"
	"database/sql"
	"errors"
)

// GithubAppRepo is a row from github_app_repos — the bridge between an
// installation and the repos it has access to.
type GithubAppRepo struct {
	ID             int64
	InstallationID int64 // FK to github_app_installations.id
	RepoID         int64 // GitHub's repo id (globally unique)
	FullName       string // "owner/repo"
	Private        bool
	DefaultBranch  sql.NullString
	CreatedAt      int64
}

// AddGithubAppRepo upserts a repo bridge row. Idempotent on repo_id —
// re-running with the same GitHub-side repo id is a no-op (the FK + the
// UNIQUE constraint take care of it).
func (db *DB) AddGithubAppRepo(ctx context.Context, r GithubAppRepo) (int64, error) {
	res, err := db.ExecContext(ctx, `
        INSERT INTO github_app_repos (
            installation_id, repo_id, full_name, private, default_branch, created_at
        ) VALUES (?, ?, ?, ?, ?, unixepoch())
        ON CONFLICT(repo_id) DO UPDATE SET
            installation_id = excluded.installation_id,
            full_name = excluded.full_name,
            private = excluded.private,
            default_branch = excluded.default_branch
    `, r.InstallationID, r.RepoID, r.FullName, boolToInt(r.Private), r.DefaultBranch)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RemoveGithubAppRepo removes a repo bridge by GitHub's repo id (matches
// the field in `installation_repositories` webhook payloads).
func (db *DB) RemoveGithubAppRepo(ctx context.Context, repoID int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM github_app_repos WHERE repo_id = ?`, repoID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetGithubRepoByFullName looks up a repo bridge by "owner/repo"
// notation. Used by the webhook receiver to map a push event's
// `repository.full_name` to a project's installation.
func (db *DB) GetGithubRepoByFullName(ctx context.Context, fullName string) (*GithubAppRepo, error) {
	var r GithubAppRepo
	var private int
	err := db.QueryRowContext(ctx, `
        SELECT id, installation_id, repo_id, full_name, private, default_branch, created_at
        FROM github_app_repos WHERE full_name = ?
    `, fullName).Scan(
		&r.ID, &r.InstallationID, &r.RepoID, &r.FullName, &private, &r.DefaultBranch, &r.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Private = private != 0
	return &r, nil
}

// ListGithubReposForInstallation returns every repo currently linked to
// an installation.
func (db *DB) ListGithubReposForInstallation(ctx context.Context, installationID int64) ([]GithubAppRepo, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT id, installation_id, repo_id, full_name, private, default_branch, created_at
        FROM github_app_repos WHERE installation_id = ? ORDER BY full_name
    `, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GithubAppRepo
	for rows.Next() {
		var r GithubAppRepo
		var private int
		if err := rows.Scan(
			&r.ID, &r.InstallationID, &r.RepoID, &r.FullName, &private, &r.DefaultBranch, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.Private = private != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindProjectsForRepoBranch returns every project that's tracking a
// (full_name, branch) combination. The webhook receiver uses this to
// know which projects to enqueue deploys for after a push.
//
// A project with an empty branch ("" — not yet set) defaults to main or
// master only, matching Disco's behavior. Explicitly-set branches must
// match exactly.
//
// A push to a repo not tracked by any project returns an empty slice
// without error — that's not unusual; users may grant the App access to
// many repos but only deploy a subset.
func (db *DB) FindProjectsForRepoBranch(ctx context.Context, fullName, branch string) ([]Project, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT id, name, github_repo, branch, github_app_installation_id,
               created_at, updated_at
        FROM projects
        WHERE github_repo = ?
          AND (branch = ? OR (branch = '' AND ? IN ('main', 'master')))
    `, fullName, branch, branch)
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
