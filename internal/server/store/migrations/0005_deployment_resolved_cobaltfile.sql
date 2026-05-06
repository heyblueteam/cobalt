-- 0005_deployment_resolved_cobaltfile.sql
--
-- Stores the cobaltfile that was actually used for a deployment, so the
-- Caddy convergence reconciler can compute desired state for the live
-- deployment without re-cloning the repo.
--
-- This is filled in by the orchestrator after Preparer parses cobalt.json
-- (or the --file override). Empty for old rows; reconciler skips those.

ALTER TABLE deployments ADD COLUMN resolved_cobaltfile TEXT;
