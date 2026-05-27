CREATE TABLE incident_notifications (
    uid              text        PRIMARY KEY,
    organization_uid text        NOT NULL REFERENCES organizations(uid)              ON DELETE CASCADE,
    incident_uid     text        NOT NULL REFERENCES incidents(uid)                  ON DELETE CASCADE,
    event_type       text        NOT NULL,
    step_uid         text        NULL     REFERENCES escalation_policy_steps(uid)    ON DELETE SET NULL,
    repeat_index     integer     NULL,
    source           text        NOT NULL,
    user_uid         text        NULL     REFERENCES users(uid)                       ON DELETE SET NULL,
    connection_uid   text        NULL     REFERENCES integration_connections(uid)     ON DELETE SET NULL,
    channel_type     text        NOT NULL,
    status           text        NOT NULL,
    skip_reason      text        NULL,
    error            text        NULL,
    job_uid          text        NULL,
    message_id       text        NULL,
    created_at       text        NOT NULL DEFAULT (datetime('now')),
    sent_at          text        NULL,
    cancelled_at     text        NULL,
    failed_at        text        NULL
);

CREATE INDEX idx_in_incident ON incident_notifications (incident_uid, created_at DESC);
CREATE INDEX idx_in_user     ON incident_notifications (user_uid,     created_at DESC) WHERE user_uid IS NOT NULL;
CREATE INDEX idx_in_org_time ON incident_notifications (organization_uid, created_at DESC);
CREATE INDEX idx_in_job      ON incident_notifications (job_uid) WHERE job_uid IS NOT NULL;
