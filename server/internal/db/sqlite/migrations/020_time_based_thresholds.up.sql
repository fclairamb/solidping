-- Replace count-based incident/recovery thresholds with wall-clock periods.
-- Spec 2026-05-08-02-time-based-confirmation-and-recovery-periods.md.
--
-- The old `incident_threshold` and `recovery_threshold` integer columns
-- coupled alert latency to `period`: tighten the check frequency from 60 s
-- to 10 s and the alert window silently shrinks from 3 minutes to 30
-- seconds. Operators reason in seconds, not ticks. We replace the count
-- fields with `confirmation_period_seconds` / `recovery_period_seconds`
-- and track the running confirmation/recovery clocks via two timestamps
-- on the check row.
--
-- The translation preserves the *effective* alerting window: 3 ticks of
-- 60 s ⇒ 180 s confirmation period. Single-failure-fires defaults
-- (threshold = 1) translate to 0 = "open immediately on first failure".
--
-- The down migration re-coarsens to ticks; some fidelity is lost when
-- the original confirmation period was shorter than `period`.

ALTER TABLE checks ADD COLUMN confirmation_period_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN recovery_period_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN first_failure_at TIMESTAMP;
ALTER TABLE checks ADD COLUMN first_success_since_failure_at TIMESTAMP;

-- Period is stored as a Go-encoded duration string (e.g. "1m0s") via the
-- timeutils.Duration scanner — but for the historical SQLite rows it is
-- a TEXT column whose value is "00:01:00" / "00:00:30" style HH:MM:SS.
-- Convert that to seconds for the multiplication below.
UPDATE checks
SET confirmation_period_seconds =
        CASE
            WHEN incident_threshold > 1 THEN
                incident_threshold * (
                    CAST(substr(period, 1, 2) AS INTEGER) * 3600
                    + CAST(substr(period, 4, 2) AS INTEGER) * 60
                    + CAST(substr(period, 7, 2) AS INTEGER)
                )
            ELSE 0
        END,
    recovery_period_seconds =
        CASE
            WHEN recovery_threshold > 1 THEN
                recovery_threshold * (
                    CAST(substr(period, 1, 2) AS INTEGER) * 3600
                    + CAST(substr(period, 4, 2) AS INTEGER) * 60
                    + CAST(substr(period, 7, 2) AS INTEGER)
                )
            ELSE 0
        END;

ALTER TABLE checks DROP COLUMN incident_threshold;
ALTER TABLE checks DROP COLUMN recovery_threshold;
