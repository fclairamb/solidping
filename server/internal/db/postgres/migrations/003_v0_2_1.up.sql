-- Per-recipient email unsubscribe (spec 2026-07-05-10). A suppression row
-- means "this email address should not receive incident/alert emails" for
-- either one specific check (check_uid set) or every check in the org
-- (check_uid NULL). Transactional emails (registration, password reset,
-- invitation, password-changed) never consult this table.
--
-- Two partial unique indexes rather than one plain unique index: Postgres
-- (like SQLite) treats each NULL as distinct under a plain unique index, so
-- two org-wide (check_uid IS NULL) rows for the same (org, email) would not
-- collide. Splitting into a "has a check" index and a "no check" index
-- makes both scopes properly unique.

create table email_suppressions (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid),
  email            text not null,
  check_uid        uuid references checks(uid),
  source           text not null check (source in ('link', 'header', 'dashboard')),
  created_at       timestamptz not null default now()
);

create unique index email_suppressions_check_scope_idx
  on email_suppressions (organization_uid, email, check_uid)
  where check_uid is not null;

create unique index email_suppressions_org_scope_idx
  on email_suppressions (organization_uid, email)
  where check_uid is null;

create index email_suppressions_org_email_idx on email_suppressions (organization_uid, email);

comment on table email_suppressions is 'Per-recipient email unsubscribe: (org, email, check_uid) opted out of incident/alert emails. check_uid NULL = all checks in the org. Transactional emails never consult this table.';
comment on column email_suppressions.check_uid is 'NULL means the suppression applies to every check in the org; otherwise scoped to this one check.';
comment on column email_suppressions.source is 'How the suppression was created: link (unsubscribe page/one-click), header (List-Unsubscribe-Post auto-submitted by the mail client), or dashboard (admin-initiated, not currently exposed by the API).';
