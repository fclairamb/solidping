-- Replace count-based incident/recovery thresholds with wall-clock periods.
-- Spec 2026-05-08-02-time-based-confirmation-and-recovery-periods.md.
--
-- Postgres stores `period` as INTERVAL, so EXTRACT(EPOCH FROM …) gives us
-- the seconds directly. The translation preserves the *effective*
-- alerting window: 3 ticks of 60 s ⇒ 180 s confirmation period.

ALTER TABLE checks ADD COLUMN confirmation_period_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN recovery_period_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN first_failure_at TIMESTAMP;
ALTER TABLE checks ADD COLUMN first_success_since_failure_at TIMESTAMP;

UPDATE checks
SET confirmation_period_seconds =
        CASE WHEN incident_threshold > 1
             THEN (incident_threshold * EXTRACT(EPOCH FROM period))::INTEGER
             ELSE 0
        END,
    recovery_period_seconds =
        CASE WHEN recovery_threshold > 1
             THEN (recovery_threshold * EXTRACT(EPOCH FROM period))::INTEGER
             ELSE 0
        END;

ALTER TABLE checks DROP COLUMN incident_threshold;
ALTER TABLE checks DROP COLUMN recovery_threshold;
