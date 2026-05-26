CREATE TABLE status_page_subscriber (
    uid               uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_uid  uuid         NOT NULL REFERENCES organizations(uid),
    status_page_uid   uuid         NOT NULL REFERENCES status_pages(uid),
    email             TEXT         NOT NULL,
    confirmed_at      TIMESTAMPTZ,
    confirm_token     TEXT         NOT NULL,
    unsubscribe_token TEXT         NOT NULL,
    scope             TEXT         NOT NULL CHECK (scope IN ('page','incident')),
    incident_uid      uuid         REFERENCES incidents(uid),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_status_page_subscriber_confirm_token
    ON status_page_subscriber(confirm_token);

CREATE UNIQUE INDEX idx_status_page_subscriber_unsub_token
    ON status_page_subscriber(unsubscribe_token);

CREATE INDEX idx_status_page_subscriber_page_confirmed
    ON status_page_subscriber(status_page_uid, confirmed_at)
    WHERE deleted_at IS NULL;

-- Prevent duplicate live subscriptions for the same (page, email, scope,
-- incident). COALESCE the nullable incident_uid so page-scoped rows collapse to
-- a single slot. Scoped to deleted_at IS NULL so a soft-deleted row can be
-- re-subscribed (the service soft-undeletes instead of inserting a duplicate).
CREATE UNIQUE INDEX idx_status_page_subscriber_live
    ON status_page_subscriber(status_page_uid, email, scope, COALESCE(incident_uid, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE deleted_at IS NULL;
