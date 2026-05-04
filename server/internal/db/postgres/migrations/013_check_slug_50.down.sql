ALTER TABLE checks DROP CONSTRAINT IF EXISTS checks_slug_check;
ALTER TABLE checks ADD CONSTRAINT checks_slug_check
  CHECK (slug IS NULL OR slug ~ '^[a-z][a-z0-9-]{3,40}$');
