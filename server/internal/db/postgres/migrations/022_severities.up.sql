-- Severity primitive for escalation steps. Spec
-- 2026-05-08-03-severity-primitive-for-escalation-steps.md.
-- See the sqlite mirror migration for the rationale.

CREATE TABLE severities (
    uid              uuid NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_uid uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    slug             text NOT NULL,
    name             text NOT NULL,
    description      text,
    channels         jsonb NOT NULL DEFAULT '[]'::jsonb,
    is_default       boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz
);

CREATE UNIQUE INDEX severities_org_slug_alive_idx
    ON severities (organization_uid, slug)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX severities_org_default_alive_idx
    ON severities (organization_uid)
    WHERE is_default AND deleted_at IS NULL;

ALTER TABLE escalation_policy_steps ADD COLUMN severity_uid uuid
    REFERENCES severities(uid) ON DELETE SET NULL;

-- Seed three defaults for every existing org.
INSERT INTO severities (organization_uid, slug, name, channels, is_default)
SELECT o.uid, 'low', 'Low', '["email"]'::jsonb, false
FROM organizations o
WHERE o.deleted_at IS NULL;

INSERT INTO severities (organization_uid, slug, name, channels, is_default)
SELECT o.uid, 'default', 'Default', '["email","slack"]'::jsonb, true
FROM organizations o
WHERE o.deleted_at IS NULL;

INSERT INTO severities (organization_uid, slug, name, channels, is_default)
SELECT o.uid, 'critical', 'Critical',
       '["email","slack","sms","voice","critical_push"]'::jsonb, false
FROM organizations o
WHERE o.deleted_at IS NULL;
