-- 0007_projects_watch_paths.sql
--
-- Optional comma-separated list of extra repo-relative sub-paths that
-- also trigger a deploy when a push touches them, in addition to the
-- project's path. Empty (the default) means none — every existing
-- project keeps working unchanged.
--
-- Real-world use: a monorepo project whose Docker build COPYs code from
-- outside its path (e.g. blue's api/ and app/ images both COPY a
-- repo-root shared/ folder). Without this, a push touching only
-- shared/ is skipped by the webhook's path filter and the fix never
-- deploys.

ALTER TABLE projects ADD COLUMN watch_paths TEXT NOT NULL DEFAULT '';
