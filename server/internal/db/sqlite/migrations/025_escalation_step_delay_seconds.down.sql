-- Reverse: delay_seconds → delay_minutes (/60).
ALTER TABLE escalation_policy_steps
    ADD COLUMN delay_minutes integer NOT NULL DEFAULT 0;

UPDATE escalation_policy_steps
    SET delay_minutes = delay_seconds / 60;

ALTER TABLE escalation_policy_steps
    DROP COLUMN delay_seconds;

-- Reverse: repeat_after_seconds → repeat_after_minutes (/60).
ALTER TABLE escalation_policies
    ADD COLUMN repeat_after_minutes integer;

UPDATE escalation_policies
    SET repeat_after_minutes = repeat_after_seconds / 60
    WHERE repeat_after_seconds IS NOT NULL;

ALTER TABLE escalation_policies
    DROP COLUMN repeat_after_seconds;
