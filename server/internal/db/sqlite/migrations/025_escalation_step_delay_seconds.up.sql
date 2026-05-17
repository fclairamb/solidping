-- escalation_policy_steps: rename delay_minutes → delay_seconds (×60).
-- modernc.org/sqlite supports ALTER TABLE ADD COLUMN and DROP COLUMN.
ALTER TABLE escalation_policy_steps
    ADD COLUMN delay_seconds integer NOT NULL DEFAULT 0;

UPDATE escalation_policy_steps
    SET delay_seconds = delay_minutes * 60;

ALTER TABLE escalation_policy_steps
    DROP COLUMN delay_minutes;

-- escalation_policies: rename repeat_after_minutes → repeat_after_seconds (×60).
ALTER TABLE escalation_policies
    ADD COLUMN repeat_after_seconds integer;

UPDATE escalation_policies
    SET repeat_after_seconds = repeat_after_minutes * 60
    WHERE repeat_after_minutes IS NOT NULL;

ALTER TABLE escalation_policies
    DROP COLUMN repeat_after_minutes;
