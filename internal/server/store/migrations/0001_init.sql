-- 0001_init.sql
--
-- Initial schema. All tables created together so we have one canonical
-- starting point. Subsequent migrations are additive only.

CREATE TABLE projects (
    id                            INTEGER PRIMARY KEY AUTOINCREMENT,
    name                          TEXT NOT NULL UNIQUE,
    github_repo                   TEXT NOT NULL,
    branch                        TEXT NOT NULL,
    github_app_installation_id    INTEGER REFERENCES github_app_installations(id) ON DELETE SET NULL,
    created_at                    INTEGER NOT NULL,
    updated_at                    INTEGER NOT NULL
);

CREATE TABLE deployments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number        INTEGER NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('queued','fetching','building','swapping','success','failed','canceled')),
    commit_sha    TEXT,
    no_cache      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    started_at    INTEGER,
    finished_at   INTEGER,
    UNIQUE (project_id, number)
);

CREATE INDEX idx_deployments_project_status ON deployments(project_id, status);

CREATE TABLE env_vars (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    key           TEXT NOT NULL,
    value         BLOB NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE (project_id, key)
);

CREATE TABLE domains (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name          TEXT NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL
);

CREATE INDEX idx_domains_project ON domains(project_id);

CREATE TABLE apikeys (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- key_hash is sha256(rawKey). Raw key is shown once on creation, then never stored.
    key_hash      TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER
);

CREATE TABLE apikey_invites (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    token         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    accepted_at   INTEGER,
    expires_at    INTEGER NOT NULL
);

CREATE TABLE github_apps (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          INTEGER NOT NULL UNIQUE,
    slug            TEXT,
    owner           TEXT NOT NULL,
    private_key     TEXT NOT NULL,
    webhook_secret  TEXT NOT NULL,
    client_id       TEXT,
    client_secret   TEXT,
    created_at      INTEGER NOT NULL
);

CREATE TABLE github_app_installations (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id            INTEGER NOT NULL REFERENCES github_apps(id) ON DELETE CASCADE,
    installation_id   INTEGER NOT NULL UNIQUE,
    account_login     TEXT NOT NULL,
    created_at        INTEGER NOT NULL
);

CREATE TABLE github_app_repos (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    installation_id INTEGER NOT NULL REFERENCES github_app_installations(id) ON DELETE CASCADE,
    repo_id         INTEGER NOT NULL UNIQUE,
    full_name       TEXT NOT NULL,
    private         INTEGER NOT NULL DEFAULT 0,
    default_branch  TEXT,
    created_at      INTEGER NOT NULL
);

CREATE INDEX idx_github_repos_installation ON github_app_repos(installation_id);
CREATE INDEX idx_github_repos_full_name ON github_app_repos(full_name);

CREATE TABLE pending_github_apps (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    state         TEXT NOT NULL UNIQUE,
    organization  TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE command_runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service       TEXT,
    command       TEXT NOT NULL,
    status        TEXT NOT NULL,
    exit_code     INTEGER,
    created_at    INTEGER NOT NULL,
    finished_at   INTEGER
);

CREATE INDEX idx_command_runs_project ON command_runs(project_id, created_at);
