-- Index email check tokens for fast lookup by JMAP handler.
-- Token is stored at $.token in the JSON config; the partial predicate keeps
-- the index small — only email checks need it.
CREATE INDEX IF NOT EXISTS idx_checks_email_token
  ON checks (json_extract(config, '$.token'))
  WHERE type = 'email' AND deleted_at IS NULL;
