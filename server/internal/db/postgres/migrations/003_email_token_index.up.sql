-- Index email check tokens for fast lookup by JMAP handler.
-- Token is stored at config->>'token' for email-type checks. The partial
-- predicate keeps the index small — only email checks need it.
CREATE INDEX IF NOT EXISTS idx_checks_email_token
  ON checks ((config->>'token'))
  WHERE type = 'email' AND deleted_at IS NULL;
