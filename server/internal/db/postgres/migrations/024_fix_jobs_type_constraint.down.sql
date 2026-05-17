-- Restore the original (too-restrictive) jobs type constraint.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check CHECK (type ~ '^[a-z][a-z0-9-]{3,20}$');
