-- 0004_deployment_cobaltfile_override.sql
--
-- Persists `cobalt deploy --file <path>` overrides on the deployment row
-- so a daemon restart between enqueue and pickup doesn't silently drop
-- the override.

ALTER TABLE deployments ADD COLUMN cobaltfile_override TEXT;
