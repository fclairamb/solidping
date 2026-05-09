-- Rollback to count-based thresholds. Re-coarsens to ticks; loses
-- fidelity when the saved seconds aren't an exact multiple of period.

ALTER TABLE checks ADD COLUMN incident_threshold INTEGER NOT NULL DEFAULT 1;
ALTER TABLE checks ADD COLUMN recovery_threshold INTEGER NOT NULL DEFAULT 1;

UPDATE checks
SET incident_threshold =
        CASE WHEN confirmation_period_seconds > 0
             THEN GREATEST(1, CEIL(confirmation_period_seconds::numeric / EXTRACT(EPOCH FROM period))::INTEGER)
             ELSE 1
        END,
    recovery_threshold =
        CASE WHEN recovery_period_seconds > 0
             THEN GREATEST(1, CEIL(recovery_period_seconds::numeric / EXTRACT(EPOCH FROM period))::INTEGER)
             ELSE 1
        END;

ALTER TABLE checks DROP COLUMN confirmation_period_seconds;
ALTER TABLE checks DROP COLUMN recovery_period_seconds;
ALTER TABLE checks DROP COLUMN first_failure_at;
ALTER TABLE checks DROP COLUMN first_success_since_failure_at;
