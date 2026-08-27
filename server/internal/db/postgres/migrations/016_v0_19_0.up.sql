-- v0.19.0 — the ONE consolidated migration for the (still unreleased) v0.19.0
-- release. 015_v0_18_0 is the last RELEASED migration (tag v0.18.3), so
-- everything this cycle produces lands here, in a single file per dialect, per
-- the repo convention documented in wiki/conventions/database.md.
--
--   SECTION: entitlement-source-split   relabel legacy admin rows to org-admin

-- ==========================================================================
-- SECTION: entitlement-source-split
--
-- Spec 2026-08-26-06 gave `source = 'admin'` two new powers it did not have
-- when these rows were written:
--
--   1. It SUPPRESSES billing pushes until a superadmin explicitly releases it.
--   2. It resolves WHOLE-ROW — a nil cap in the payload now means UNLIMITED
--      rather than "not stated, fall through to the deployment default".
--
-- Every row that can currently hold 'admin' predates that spec, because until
-- it landed EVERY non-service write got 'admin': org admins, self-hosted
-- operators and the CLI alike. Such rows are routinely PARTIAL — the API has
-- never required a complete payload on PUT, and the CLI coverage test is
-- itself an example, writing three caps and leaving six nil.
--
-- Left alone, power (2) silently ESCALATES those rows on the first resolve
-- after deploy: on SaaS maxChecksPerMinute 10 -> unlimited (the very cap this
-- spec exists to manage), maxUsers 5 -> unlimited, maxSlos 2 -> unlimited, and
-- maxSmsPerMonth / maxCallsPerMonth / maxWhatsappPerMonth 0 -> unlimited,
-- which is unbounded spend on the instance's own Twilio/Meta credentials. On
-- self-hosted, where that door is the NORMAL one because
-- entitlements.admin_writes_enabled defaults to true, maxUsers 30 -> unlimited
-- lifts the seat guard. No operator asked for any of that, and it lands long
-- before anyone opens the new editor, so a release note is not a control.
--
-- Relabelling is semantically lossless: by construction these WERE org-scoped
-- writes, and 'org-admin' is exactly the old behaviour under a new name — same
-- paid plan weight, same null-fill resolution, still overwritten by billing's
-- next reconcile. A superadmin who genuinely wants an override re-saves it in
-- the editor, which is one click.
--
-- Only ONE place holds the live source: org_entitlements.payload->>'source'.
-- (org_entitlement_audits.source is a different thing — the historical log of
-- what each past write claimed. Nothing reads it for behaviour; it is rendered
-- as a badge. It is deliberately NOT rewritten: falsifying an audit trail to
-- match today's vocabulary is worse than the vocabulary drifting.) The insert
-- below bridges the two, so an operator looking at a relabelled org sees why.
-- ==========================================================================

-- Audit FIRST, so before_snapshot still describes the pre-relabel payload.
insert into org_entitlement_audits (
  uid, organization_uid, source, actor, before_snapshot, after_snapshot, reason
)
select
  gen_random_uuid(),
  organization_uid,
  'migration:org-admin-relabel',
  'migration:016_v0_19_0',
  payload,
  jsonb_set(payload, '{source}', '"org-admin"'),
  'Upgrade to v0.19.0 relabelled this row from source=admin to source=org-admin. '
  || 'It was written through the org-scoped API before the superadmin editor existed, '
  || 'so it keeps exactly the behaviour it always had: unset caps fall back to the '
  || 'deployment defaults, and the billing service can still update it. To make it a '
  || 'real override that outranks billing, re-save it from Server -> Entitlements.'
from org_entitlements
where payload->>'source' = 'admin';

update org_entitlements
set payload    = jsonb_set(payload, '{source}', '"org-admin"'),
    updated_at = now()
where payload->>'source' = 'admin';
