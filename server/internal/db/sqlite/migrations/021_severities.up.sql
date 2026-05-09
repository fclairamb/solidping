-- Severity primitive for escalation steps. Spec
-- 2026-05-08-03-severity-primitive-for-escalation-steps.md.
--
-- A severity bundles a set of channel-types (email/sms/voice/push/...)
-- so an escalation step can say "page critically" without enumerating
-- three sequential single-channel steps. Severities are scoped per
-- organization and seeded with three rows: low / default / critical.
-- Operators can edit, add, or delete any non-default row.

CREATE TABLE severities (
    uid              TEXT NOT NULL PRIMARY KEY,
    organization_uid TEXT NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    slug             TEXT NOT NULL,
    name             TEXT NOT NULL,
    description      TEXT,
    -- channels is a JSON array of channel-type strings, e.g.
    -- ["email","slack"] or ["email","slack","sms","voice","critical_push"].
    -- Validation happens in the service layer against the union of
    -- ConnectionType values and the direct-channel synthetic types
    -- (sms/voice/push/critical_push).
    channels         TEXT NOT NULL DEFAULT '[]',
    is_default       INTEGER NOT NULL DEFAULT 0,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at       TIMESTAMP
);

-- A severity slug is unique within an org while the row is live.
CREATE UNIQUE INDEX severities_org_slug_alive_idx
    ON severities (organization_uid, slug)
    WHERE deleted_at IS NULL;

-- Exactly one default per org (the partial index makes the constraint
-- recoverable: a soft-deleted default doesn't block the next one).
CREATE UNIQUE INDEX severities_org_default_alive_idx
    ON severities (organization_uid)
    WHERE is_default = 1 AND deleted_at IS NULL;

ALTER TABLE escalation_policy_steps ADD COLUMN severity_uid TEXT
    REFERENCES severities(uid) ON DELETE SET NULL;

-- Seed the three default severities for every existing org.
INSERT INTO severities (uid, organization_uid, slug, name, channels, is_default)
SELECT
    lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4'
        || substr(lower(hex(randomblob(2))), 2)
        || '-' || lower(hex(randomblob(2)))
        || '-' || lower(hex(randomblob(6))),
    o.uid,
    'low',
    'Low',
    '["email"]',
    0
FROM organizations o
WHERE o.deleted_at IS NULL;

INSERT INTO severities (uid, organization_uid, slug, name, channels, is_default)
SELECT
    lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4'
        || substr(lower(hex(randomblob(2))), 2)
        || '-' || lower(hex(randomblob(2)))
        || '-' || lower(hex(randomblob(6))),
    o.uid,
    'default',
    'Default',
    '["email","slack"]',
    1
FROM organizations o
WHERE o.deleted_at IS NULL;

INSERT INTO severities (uid, organization_uid, slug, name, channels, is_default)
SELECT
    lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4'
        || substr(lower(hex(randomblob(2))), 2)
        || '-' || lower(hex(randomblob(2)))
        || '-' || lower(hex(randomblob(6))),
    o.uid,
    'critical',
    'Critical',
    '["email","slack","sms","voice","critical_push"]',
    0
FROM organizations o
WHERE o.deleted_at IS NULL;
