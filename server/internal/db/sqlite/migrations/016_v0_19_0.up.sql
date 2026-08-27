-- v0.19.0 — the ONE consolidated migration for the (still unreleased) v0.19.0
-- release. 015_v0_18_0 is the last RELEASED migration (tag v0.18.3), so
-- everything this cycle produces lands here, in a single file per dialect, per
-- the repo convention documented in wiki/conventions/database.md.
--
--   SECTION: entitlement-source-split   relabel legacy admin rows to org-admin

-- ==========================================================================
-- SECTION: entitlement-source-split
--
-- SQLite half of the PostgreSQL migration of the same name — see
-- postgres/migrations/016_v0_19_0.up.sql for the full rationale. In short:
-- spec 2026-08-26-06 made `source = 'admin'` both suppress billing pushes and
-- resolve WHOLE-ROW (nil cap = unlimited). Every existing 'admin' row predates
-- that spec — until it landed, every non-service write got 'admin' — and such
-- rows are routinely partial, so leaving them would silently turn their unset
-- caps into "unlimited" on the first resolve after deploy. On self-hosted,
-- which is the deployment SQLite actually serves, that means the default
-- maxUsers seat guard (30) quietly disappears for any org whose limits an
-- operator had ever touched.
--
-- 'org-admin' is the old behaviour under a new name, so the relabel is
-- lossless.
--
-- The live source lives in exactly one place, org_entitlements.payload's
-- `source` key. org_entitlement_audits.source is the historical log of what
-- past writes claimed, is read for display only, and is deliberately left
-- alone rather than rewritten to match today's vocabulary.
-- ==========================================================================

-- Audit FIRST, so before_snapshot still describes the pre-relabel payload.
-- SQLite has no uuid generator, so the v4 shape is assembled from randomblob —
-- the standard idiom, and these uids are only ever read back as opaque keys.
insert into org_entitlement_audits (
  uid, organization_uid, source, actor, before_snapshot, after_snapshot, reason
)
select
  lower(
    hex(randomblob(4)) || '-' ||
    hex(randomblob(2)) || '-4' ||
    substr(hex(randomblob(2)), 2) || '-' ||
    substr('89ab', abs(random()) % 4 + 1, 1) ||
    substr(hex(randomblob(2)), 2) || '-' ||
    hex(randomblob(6))
  ),
  organization_uid,
  'migration:org-admin-relabel',
  'migration:016_v0_19_0',
  payload,
  json_set(payload, '$.source', 'org-admin'),
  'Upgrade to v0.19.0 relabelled this row from source=admin to source=org-admin. '
  || 'It was written through the org-scoped API before the superadmin editor existed, '
  || 'so it keeps exactly the behaviour it always had: unset caps fall back to the '
  || 'deployment defaults, and the billing service can still update it. To make it a '
  || 'real override that outranks billing, re-save it from Server -> Entitlements.'
from org_entitlements
where json_extract(payload, '$.source') = 'admin';

update org_entitlements
set payload    = json_set(payload, '$.source', 'org-admin'),
    updated_at = datetime('now')
where json_extract(payload, '$.source') = 'admin';
