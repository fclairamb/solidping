CREATE TABLE user_contacts (
    uid              uuid        PRIMARY KEY,
    user_uid         uuid        NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    organization_uid uuid        NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    type             text        NOT NULL,
    value            text        NOT NULL,
    label            text        NOT NULL DEFAULT '',
    verified_at      timestamptz NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz NULL,
    UNIQUE (user_uid, organization_uid, type, value)
);

CREATE INDEX idx_uc_user_org ON user_contacts (user_uid, organization_uid) WHERE deleted_at IS NULL;

CREATE TABLE user_notification_routes (
    uid          uuid    PRIMARY KEY,
    user_uid     uuid    NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    org_uid      uuid    NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    contact_uid  uuid    NOT NULL REFERENCES user_contacts(uid) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT true,
    position     int     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_unr_contact ON user_notification_routes (contact_uid);
CREATE INDEX idx_unr_user_org ON user_notification_routes (user_uid, org_uid);
