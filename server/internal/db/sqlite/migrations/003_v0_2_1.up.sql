-- Per-recipient email unsubscribe (spec 2026-07-05-10). A suppression row
-- means "this email address should not receive incident/alert emails" for
-- either one specific check (check_uid set) or every check in the org
-- (check_uid NULL). Transactional emails (registration, password reset,
-- invitation, password-changed) never consult this table.
--
-- Two partial unique indexes rather than one plain unique index: SQLite
-- (like Postgres) treats each NULL as distinct under a plain unique index,
-- so two org-wide (check_uid IS NULL) rows for the same (org, email) would
-- not collide. Splitting into a "has a check" index and a "no check" index
-- makes both scopes properly unique. Partial indexes (WHERE clause) have
-- been supported since SQLite 3.8.0, well within the bundled engine.

create table email_suppressions (
  uid              text primary key,
  organization_uid text not null references organizations(uid),
  email            text not null,
  check_uid        text references checks(uid),
  source           text not null check (source in ('link', 'header', 'dashboard')),
  created_at       text not null default (datetime('now'))
);

create unique index email_suppressions_check_scope_idx
  on email_suppressions (organization_uid, email, check_uid)
  where check_uid is not null;

create unique index email_suppressions_org_scope_idx
  on email_suppressions (organization_uid, email)
  where check_uid is null;

create index email_suppressions_org_email_idx on email_suppressions (organization_uid, email);
