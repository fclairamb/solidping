-- Rollback for the effective_scheduled_at re-anchor (spec 2026-07-01-02).
--
-- Deliberate no-op: the up migration collapsed delay-inflated offsets into
-- scheduled_at, and the old values are unrecoverable — and were wrong (they
-- stranded jobs beyond the claim window). Reverting the code alone is safe:
-- every job's offset is recomputed on its next post-exec release.

select 1; -- no-op
