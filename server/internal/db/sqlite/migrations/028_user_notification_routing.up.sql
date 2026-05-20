CREATE TABLE user_contacts (
    uid              text PRIMARY KEY,
    user_uid         text NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    organization_uid text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    type             text NOT NULL,
    value            text NOT NULL,
    label            text NOT NULL DEFAULT '',
    verified_at      text NULL,
    created_at       text NOT NULL DEFAULT (datetime('now')),
    updated_at       text NOT NULL DEFAULT (datetime('now')),
    deleted_at       text NULL,
    UNIQUE (user_uid, organization_uid, type, value)
);

CREATE INDEX idx_uc_user_org ON user_contacts (user_uid, organization_uid) WHERE deleted_at IS NULL;

CREATE TABLE user_notification_routes (
    uid          text    PRIMARY KEY,
    user_uid     text    NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    org_uid      text    NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    contact_uid  text    NOT NULL REFERENCES user_contacts(uid) ON DELETE CASCADE,
    enabled      integer NOT NULL DEFAULT 1,
    position     integer NOT NULL DEFAULT 0,
    created_at   text    NOT NULL DEFAULT (datetime('now')),
    updated_at   text    NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_unr_contact ON user_notification_routes (contact_uid);
CREATE INDEX idx_unr_user_org ON user_notification_routes (user_uid, org_uid);
