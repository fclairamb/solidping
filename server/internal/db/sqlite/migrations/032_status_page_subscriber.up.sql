CREATE TABLE status_page_subscriber (
    uid               VARCHAR(36)  PRIMARY KEY,
    organization_uid  VARCHAR(36)  NOT NULL REFERENCES organizations(uid),
    status_page_uid   VARCHAR(36)  NOT NULL REFERENCES status_pages(uid),
    email             TEXT         NOT NULL,
    confirmed_at      TIMESTAMP,
    confirm_token     TEXT         NOT NULL,
    unsubscribe_token TEXT         NOT NULL,
    scope             TEXT         NOT NULL CHECK (scope IN ('page','incident')),
    incident_uid      VARCHAR(36)  REFERENCES incidents(uid),
    created_at        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        TIMESTAMP
);

CREATE UNIQUE INDEX idx_status_page_subscriber_confirm_token
    ON status_page_subscriber(confirm_token);

CREATE UNIQUE INDEX idx_status_page_subscriber_unsub_token
    ON status_page_subscriber(unsubscribe_token);

CREATE INDEX idx_status_page_subscriber_page_confirmed
    ON status_page_subscriber(status_page_uid, confirmed_at);

-- Prevent duplicate live subscriptions for the same (page, email, scope,
-- incident). COALESCE the nullable incident_uid so page-scoped rows collapse to
-- a single slot. Scoped to deleted_at IS NULL so a soft-deleted row can be
-- re-subscribed (the service soft-undeletes instead of inserting a duplicate).
CREATE UNIQUE INDEX idx_status_page_subscriber_live
    ON status_page_subscriber(status_page_uid, email, scope, COALESCE(incident_uid, ''))
    WHERE deleted_at IS NULL;
