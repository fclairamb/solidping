CREATE TABLE status_updates (
    uid              VARCHAR(36)  PRIMARY KEY,
    organization_uid VARCHAR(36)  NOT NULL REFERENCES organizations(uid),
    status_page_uid  VARCHAR(36)  NOT NULL REFERENCES status_pages(uid),
    section_uid      VARCHAR(36)  REFERENCES status_page_sections(uid),
    check_uid        VARCHAR(36)  REFERENCES checks(uid),
    incident_uid     VARCHAR(36)  REFERENCES incidents(uid),
    title            TEXT         NOT NULL,
    body_markdown    TEXT         NOT NULL,
    link_url         TEXT,
    kind             TEXT         NOT NULL
                                  CHECK (kind IN (
                                      'investigating','identified','monitoring',
                                      'resolved','maintenance','info'
                                  )),
    published_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    author_uid       VARCHAR(36)  NOT NULL REFERENCES users(uid),
    created_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at       TIMESTAMP
);

CREATE INDEX idx_status_updates_org_page_pub
    ON status_updates(organization_uid, status_page_uid, published_at DESC);

CREATE INDEX idx_status_updates_incident
    ON status_updates(incident_uid);

CREATE INDEX idx_status_updates_check
    ON status_updates(check_uid);
