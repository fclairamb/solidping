-- Rollback to count-based thresholds. Re-coarsens to ticks; loses
-- fidelity when the saved seconds aren't an exact multiple of period.

ALTER TABLE checks ADD COLUMN incident_threshold INTEGER NOT NULL DEFAULT 1;
ALTER TABLE checks ADD COLUMN recovery_threshold INTEGER NOT NULL DEFAULT 1;

UPDATE checks
SET incident_threshold =
        CASE
            WHEN confirmation_period_seconds > 0 THEN
                MAX(1, (confirmation_period_seconds + (
                    CAST(substr(period, 1, 2) AS INTEGER) * 3600
                    + CAST(substr(period, 4, 2) AS INTEGER) * 60
                    + CAST(substr(period, 7, 2) AS INTEGER) - 1
                )) / NULLIF(
                    CAST(substr(period, 1, 2) AS INTEGER) * 3600
                    + CAST(substr(period, 4, 2) AS INTEGER) * 60
                    + CAST(substr(period, 7, 2) AS INTEGER), 0
                ))
            ELSE 1
        END,
    recovery_threshold =
        CASE
            WHEN recovery_period_seconds > 0 THEN
                MAX(1, (recovery_period_seconds + (
                    CAST(substr(period, 1, 2) AS INTEGER) * 3600
                    + CAST(substr(period, 4, 2) AS INTEGER) * 60
                    + CAST(substr(period, 7, 2) AS INTEGER) - 1
                )) / NULLIF(
                    CAST(substr(period, 1, 2) AS INTEGER) * 3600
                    + CAST(substr(period, 4, 2) AS INTEGER) * 60
                    + CAST(substr(period, 7, 2) AS INTEGER), 0
                ))
            ELSE 1
        END;

ALTER TABLE checks DROP COLUMN confirmation_period_seconds;
ALTER TABLE checks DROP COLUMN recovery_period_seconds;
ALTER TABLE checks DROP COLUMN first_failure_at;
ALTER TABLE checks DROP COLUMN first_success_since_failure_at;
