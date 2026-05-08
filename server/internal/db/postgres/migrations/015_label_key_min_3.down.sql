ALTER TABLE labels DROP CONSTRAINT IF EXISTS labels_key_check;
ALTER TABLE labels ADD CONSTRAINT labels_key_check
  CHECK (key ~ '^[a-z][a-z0-9-]{3,50}$');
