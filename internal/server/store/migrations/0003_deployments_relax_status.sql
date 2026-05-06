-- 0003_deployments_relax_status.sql
--
-- Drops the CHECK constraint on deployments.status so we can add new
-- statuses ('skipped' for now, more later) without further DDL. App code
-- is the source of truth for valid statuses — see pkg/cobaltapi.State.
--
-- SQLite doesn't support modifying CHECK in place, so we rebuild the
-- table. No foreign keys reference deployments, so no FK-disable dance.

CREATE TABLE deployments_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number        INTEGER NOT NULL,
    status        TEXT NOT NULL,
    commit_sha    TEXT,
    no_cache      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    started_at    INTEGER,
    finished_at   INTEGER,
    UNIQUE (project_id, number)
);

INSERT INTO deployments_new (id, project_id, number, status, commit_sha, no_cache, created_at, started_at, finished_at)
SELECT id, project_id, number, status, commit_sha, no_cache, created_at, started_at, finished_at
FROM deployments;

DROP TABLE deployments;
ALTER TABLE deployments_new RENAME TO deployments;

CREATE INDEX idx_deployments_project_status ON deployments(project_id, status);
