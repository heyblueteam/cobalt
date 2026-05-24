-- 0006_projects_path
--
-- Add an optional sub-path inside the repo where the project's
-- cobalt.json + build context live. Empty (default) means the repo
-- root, matching the previous behavior — every existing project keeps
-- working without migration of its data.
--
-- Use case: monorepos that ship multiple deploys from one repo. A
-- `path = "api"` project reads its cobalt.json from `<repo>/api/cobalt.json`
-- and resolves Dockerfile / Context paths inside that subtree.

ALTER TABLE projects ADD COLUMN path TEXT NOT NULL DEFAULT '';
