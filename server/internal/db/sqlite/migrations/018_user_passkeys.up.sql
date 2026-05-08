-- SQLite mirror of the postgres user_passkeys migration. Bytes are stored
-- as BLOB, JSON columns as TEXT, timestamps as TEXT (ISO-8601). The bun
-- driver round-trips these to the same Go types as postgres.

CREATE TABLE user_passkeys (
    uid                text PRIMARY KEY,
    user_uid           text NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    name               text NOT NULL,
    credential_id      blob NOT NULL,
    public_key         blob NOT NULL,
    aaguid             text,
    sign_count         integer NOT NULL DEFAULT 0,
    transports         text,
    backup_eligible    integer NOT NULL DEFAULT 0,
    backup_state       integer NOT NULL DEFAULT 0,
    user_verified      integer NOT NULL DEFAULT 0,
    attestation_format text,
    last_used_at       text,
    created_at         text NOT NULL DEFAULT (datetime('now')),
    updated_at         text NOT NULL DEFAULT (datetime('now')),
    deleted_at         text,
    UNIQUE (user_uid, credential_id)
);

CREATE INDEX idx_user_passkeys_credential_id ON user_passkeys (credential_id);
CREATE INDEX idx_user_passkeys_user_uid_active ON user_passkeys (user_uid)
    WHERE deleted_at IS NULL;
