-- 0002_github_app_extras.sql
--
-- Adds the columns the github subsystem needs that weren't in the initial
-- schema: installation token cache, app display fields, and an explicit
-- expiry on pending app registrations.

ALTER TABLE github_apps ADD COLUMN name TEXT;
ALTER TABLE github_apps ADD COLUMN html_url TEXT;

ALTER TABLE github_app_installations ADD COLUMN access_token TEXT;
ALTER TABLE github_app_installations ADD COLUMN access_token_expires_at INTEGER;

ALTER TABLE pending_github_apps ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
