package store

import (
	"context"
	"errors"
	"fmt"

	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

type DB struct {
	*rqlitehttp.Client
}

var ErrProjectNameTaken = errors.New("store: project name already in use")

var ErrNotFound = errors.New("store: not found")

func Open(url string) (*DB, error) {
	if url == "" {
		return nil, errors.New("store.Open: url is required")
	}
	client, err := rqlitehttp.NewClient(url, nil)
	if err != nil {
		return nil, fmt.Errorf("store.Open: %w", err)
	}
	return &DB{Client: client}, nil
}

func (db *DB) Ping(ctx context.Context) error {
	_, err := db.Ready(ctx, nil)
	return err
}

func (db *DB) InitSchema(ctx context.Context) error {
	stmts := rqlitehttp.NewSQLStatementsFromStrings([]string{
		`CREATE TABLE IF NOT EXISTS projects (
			id                            INTEGER PRIMARY KEY AUTOINCREMENT,
			name                          TEXT NOT NULL UNIQUE,
			github_repo                   TEXT NOT NULL,
			branch                        TEXT NOT NULL,
			github_app_installation_id    INTEGER,
			created_at                    INTEGER NOT NULL,
			updated_at                    INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS deployments (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			number        INTEGER NOT NULL,
			status        TEXT NOT NULL,
			commit_sha    TEXT,
			no_cache      INTEGER NOT NULL DEFAULT 0,
			cobaltfile_override TEXT,
			resolved_cobaltfile TEXT,
			created_at    INTEGER NOT NULL,
			started_at    INTEGER,
			finished_at   INTEGER,
			UNIQUE (project_id, number)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_deployments_project_status ON deployments(project_id, status)`,
		`CREATE TABLE IF NOT EXISTS env_vars (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			key           TEXT NOT NULL,
			value         BLOB NOT NULL,
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL,
			UNIQUE (project_id, key)
		)`,
		`CREATE TABLE IF NOT EXISTS domains (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			name          TEXT NOT NULL UNIQUE,
			created_at    INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_domains_project ON domains(project_id)`,
		`CREATE TABLE IF NOT EXISTS apikeys (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			key_hash      TEXT NOT NULL UNIQUE,
			name          TEXT NOT NULL,
			created_at    INTEGER NOT NULL,
			last_used_at  INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS apikey_invites (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			token         TEXT NOT NULL UNIQUE,
			name          TEXT NOT NULL,
			created_at    INTEGER NOT NULL,
			accepted_at   INTEGER,
			expires_at    INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS github_apps (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			app_id          INTEGER NOT NULL UNIQUE,
			slug            TEXT,
			owner           TEXT NOT NULL,
			private_key     TEXT NOT NULL,
			webhook_secret  TEXT NOT NULL,
			client_id       TEXT,
			client_secret   TEXT,
			name            TEXT,
			html_url        TEXT,
			created_at      INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS github_app_installations (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			app_id            INTEGER NOT NULL REFERENCES github_apps(id) ON DELETE CASCADE,
			installation_id   INTEGER NOT NULL UNIQUE,
			account_login     TEXT NOT NULL,
			access_token      TEXT,
			access_token_expires_at INTEGER,
			created_at        INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS github_app_repos (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			installation_id INTEGER NOT NULL REFERENCES github_app_installations(id) ON DELETE CASCADE,
			repo_id         INTEGER NOT NULL UNIQUE,
			full_name       TEXT NOT NULL,
			private         INTEGER NOT NULL DEFAULT 0,
			default_branch  TEXT,
			created_at      INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_github_repos_installation ON github_app_repos(installation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_github_repos_full_name ON github_app_repos(full_name)`,
		`CREATE TABLE IF NOT EXISTS pending_github_apps (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			state         TEXT NOT NULL UNIQUE,
			organization  TEXT NOT NULL,
			created_at    INTEGER NOT NULL,
			expires_at    INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS command_runs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			service       TEXT,
			command       TEXT NOT NULL,
			status        TEXT NOT NULL,
			exit_code     INTEGER,
			created_at    INTEGER NOT NULL,
			finished_at   INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_command_runs_project ON command_runs(project_id, created_at)`,
	})
	_, err := db.Execute(ctx, stmts, nil)
	return err
}
