-- Relax the jobs.type check constraint to allow underscores and longer names.
-- The original pattern ('^[a-z][a-z0-9-]{3,20}$') excluded underscores, which
-- prevented job types like 'escalation_step' and 'notification' from being
-- inserted. The new pattern matches [a-z][a-z0-9_-]{3,49} to cover all current
-- and future job types without arbitrary truncation.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check CHECK (type ~ '^[a-z][a-z0-9_-]{2,49}$');
