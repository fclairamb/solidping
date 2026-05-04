-- Bump check slug max length from 40 to 50 to allow more descriptive identifiers
-- (e.g., "prod-eu-west-payment-api"). Drop and recreate the CHECK constraint.

ALTER TABLE checks DROP CONSTRAINT IF EXISTS checks_slug_check;
ALTER TABLE checks ADD CONSTRAINT checks_slug_check
  CHECK (slug IS NULL OR slug ~ '^[a-z][a-z0-9-]{2,49}$');
