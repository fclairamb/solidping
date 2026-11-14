-- ==========================================================================
-- SECTION: email-token-unique
-- Spec 2026-11-14-01.
-- ==========================================================================
-- Inbound mail is matched to its email check by config.token alone, across
-- every organization, and a caller could set that token. Two live email checks
-- sharing one would share an inbound address. First every duplicate but the
-- oldest (created_at, then uid) gets a fresh 48-hex token, then the lookup
-- index becomes unique. The oldest row is never updated, so the EXISTS below
-- gives the same answer whatever order SQLite updates the rows in. Replayable:
-- the update matches nothing once tokens are unique.
update checks
set config = json_set(config, '$.token', lower(hex(randomblob(24))))
where type = 'email'
  and deleted_at is null
  and json_extract(config, '$.token') is not null
  and exists (
    select 1 from checks o
    where o.type = 'email'
      and o.deleted_at is null
      and json_extract(o.config, '$.token') = json_extract(checks.config, '$.token')
      and (o.created_at < checks.created_at or (o.created_at = checks.created_at and o.uid < checks.uid))
  );

drop index if exists idx_checks_email_token;

create unique index if not exists idx_checks_email_token_unique on checks (json_extract(config, '$.token'))
  where type = 'email' and deleted_at is null;
