CREATE TABLE status_updates (
    uid              uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_uid uuid         NOT NULL REFERENCES organizations(uid),
    status_page_uid  uuid         NOT NULL REFERENCES status_pages(uid),
    section_uid      uuid         REFERENCES status_page_sections(uid),
    check_uid        uuid         REFERENCES checks(uid),
    incident_uid     uuid         REFERENCES incidents(uid),
    title            TEXT         NOT NULL,
    body_markdown    TEXT         NOT NULL,
    link_url         TEXT,
    kind             TEXT         NOT NULL
                                  CHECK (kind IN (
                                      'investigating','identified','monitoring',
                                      'resolved','maintenance','info'
                                  )),
    published_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    author_uid       uuid         NOT NULL REFERENCES users(uid),
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ
);

CREATE INDEX idx_status_updates_org_page_pub
    ON status_updates(organization_uid, status_page_uid, published_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_status_updates_incident
    ON status_updates(incident_uid)
    WHERE incident_uid IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX idx_status_updates_check
    ON status_updates(check_uid)
    WHERE check_uid IS NOT NULL AND deleted_at IS NULL;
