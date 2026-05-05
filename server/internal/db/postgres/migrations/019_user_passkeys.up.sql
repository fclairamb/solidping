-- Per-user WebAuthn / passkey credentials. Public keys, credential IDs,
-- and metadata for resident-key flows. Public keys are not secrets; no
-- encryption-at-rest envelope is needed. ON DELETE CASCADE keeps the
-- table clean when a user is hard-deleted.

CREATE TABLE user_passkeys (
    uid                uuid PRIMARY KEY,
    user_uid           uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    name               text NOT NULL,
    credential_id      bytea NOT NULL,
    public_key         bytea NOT NULL,
    aaguid             text,
    sign_count         bigint NOT NULL DEFAULT 0,
    transports         jsonb,
    backup_eligible    boolean NOT NULL DEFAULT false,
    backup_state       boolean NOT NULL DEFAULT false,
    user_verified      boolean NOT NULL DEFAULT false,
    attestation_format text,
    last_used_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    deleted_at         timestamptz,
    UNIQUE (user_uid, credential_id)
);

CREATE INDEX idx_user_passkeys_credential_id ON user_passkeys (credential_id);
CREATE INDEX idx_user_passkeys_user_uid_active ON user_passkeys (user_uid)
    WHERE deleted_at IS NULL;
