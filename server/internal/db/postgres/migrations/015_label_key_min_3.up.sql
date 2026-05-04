-- Allow 3-char label keys (e.g. "env", "app", "qa") that were previously
-- rejected by the {3,50} repeat. Keep the leading-letter requirement and
-- the 50-char cap; only the lower bound moves from 4 total to 3 total.

ALTER TABLE labels DROP CONSTRAINT IF EXISTS labels_key_check;
ALTER TABLE labels ADD CONSTRAINT labels_key_check
  CHECK (key ~ '^[a-z][a-z0-9-]{2,50}$');
