package store

import (
	"context"
	"errors"
)

type GithubAppRepo struct {
	ID             int64
	InstallationID int64
	RepoID         int64
	FullName       string
	Private        bool
	DefaultBranch  string
	CreatedAt      int64
}

func (db *DB) AddGithubAppRepo(ctx context.Context, r GithubAppRepo) (int64, error) {
	defaultBranch := r.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = ""
	}
	resp, err := db.ExecuteSingle(ctx, `
        INSERT INTO github_app_repos (
            installation_id, repo_id, full_name, private, default_branch, created_at
        ) VALUES (?, ?, ?, ?, ?, strftime('%s', 'now'))
        ON CONFLICT(repo_id) DO UPDATE SET
            installation_id = excluded.installation_id,
            full_name = excluded.full_name,
            private = excluded.private,
            default_branch = excluded.default_branch
    `, r.InstallationID, r.RepoID, r.FullName, boolToInt(r.Private), defaultBranch)
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].LastInsertID, nil
}

func (db *DB) RemoveGithubAppRepo(ctx context.Context, repoID int64) error {
	resp, err := db.ExecuteSingle(ctx, `DELETE FROM github_app_repos WHERE repo_id = ?`, repoID)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) GetGithubRepoByFullName(ctx context.Context, fullName string) (*GithubAppRepo, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, installation_id, repo_id, full_name, private, default_branch, created_at
        FROM github_app_repos WHERE full_name = ?
    `, fullName)
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
	var r GithubAppRepo
	r.ID = toInt64(row[0])
	r.InstallationID = toInt64(row[1])
	r.RepoID = toInt64(row[2])
	r.FullName = toString(row[3])
	r.Private = toInt64(row[4]) != 0
	if row[5] != nil {
		r.DefaultBranch = toString(row[5])
	}
	r.CreatedAt = toInt64(row[6])
	return &r, nil
}

func (db *DB) ListGithubReposForInstallation(ctx context.Context, installationID int64) ([]GithubAppRepo, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, installation_id, repo_id, full_name, private, default_branch, created_at
        FROM github_app_repos WHERE installation_id = ? ORDER BY full_name
    `, installationID)
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
	out := make([]GithubAppRepo, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		var r GithubAppRepo
		r.ID = toInt64(row[0])
		r.InstallationID = toInt64(row[1])
		r.RepoID = toInt64(row[2])
		r.FullName = toString(row[3])
		r.Private = toInt64(row[4]) != 0
		if row[5] != nil {
			r.DefaultBranch = toString(row[5])
		}
		r.CreatedAt = toInt64(row[6])
		out = append(out, r)
	}
	return out, nil
}

func (db *DB) FindProjectsForRepoBranch(ctx context.Context, fullName, branch string) ([]Project, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, name, github_repo, branch, github_app_installation_id,
               created_at, updated_at
        FROM projects
        WHERE github_repo = ?
          AND (branch = ? OR (branch = '' AND ? IN ('main', 'master')))
    `, fullName, branch, branch)
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
		var p Project
		p.ID = toInt64(row[0])
		p.Name = toString(row[1])
		p.GithubRepo = toString(row[2])
		p.Branch = toString(row[3])
		if row[4] != nil {
			p.GithubAppInstallationID.Int64 = toInt64(row[4])
			p.GithubAppInstallationID.Valid = true
		}
		p.CreatedAt = toInt64(row[5])
		p.UpdatedAt = toInt64(row[6])
		out = append(out, p)
	}
	return out, nil
}
