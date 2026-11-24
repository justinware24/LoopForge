-- The scheme a personal Git credential is bound to.
--
-- A self-hosted GitLab commonly serves its API and its clone URLs over plain
-- HTTP on a port of its own. The credential now records the scheme it was
-- written with, so the control plane reaches the API on the same transport
-- the user registered the host on.
--
-- Existing rows keep working: an https credential keeps the identity it has
-- always had, and a credential with no scheme means https.
--
-- MySQL 8.0 does not support ADD COLUMN IF NOT EXISTS, so this script cannot
-- run twice; check information_schema.columns before running it.

ALTER TABLE user_git_http_credentials
  ADD COLUMN scheme VARCHAR(8) NOT NULL DEFAULT 'https';
