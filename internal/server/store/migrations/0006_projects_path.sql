-- 0006_projects_path.sql
--
-- Optional repo-relative sub-path that scopes a project's deploy to a
-- sub-directory of its GitHub repo. cobalt.json + Dockerfile contexts
-- are resolved relative to <repo>/<path>. Empty (the default) means
-- repo root — every existing project keeps working unchanged.
--
-- Real-world use: one GitHub repo hosting multiple cobalt projects (a
-- monorepo with api/ and services/web/ shipping as two projects against
-- the same repo + branch). Webhook dispatch is filtered by Project.Path
-- so a push that only touches one sub-tree doesn't redeploy the other.

ALTER TABLE projects ADD COLUMN path TEXT NOT NULL DEFAULT '';
