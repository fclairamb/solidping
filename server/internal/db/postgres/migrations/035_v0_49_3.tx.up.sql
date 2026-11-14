-- ==========================================================================
-- SECTION: email-token-unique
-- Spec 2026-11-14-01.
-- ==========================================================================
-- Inbound mail is matched to its email check by config.token alone, across
-- every organization, and a caller could set that token. Two live email checks
-- sharing one would share an inbound address. First every duplicate but the
-- oldest (created_at, then uid) gets a fresh 48-hex token, then the lookup
-- index becomes unique. Replayable: the update matches nothing once tokens are
-- unique.
update checks c
set config = jsonb_set(
  c.config, '{token}',
  to_jsonb(replace(gen_random_uuid()::text, '-', '')
    || substr(replace(gen_random_uuid()::text, '-', ''), 1, 16)))
where c.type = 'email'
  and c.deleted_at is null
  and c.config->>'token' is not null
  and exists (
    select 1 from checks o
    where o.type = 'email'
      and o.deleted_at is null
      and o.config->>'token' = c.config->>'token'
      and (o.created_at < c.created_at or (o.created_at = c.created_at and o.uid < c.uid))
  );

--bun:split

drop index if exists idx_checks_email_token;

--bun:split

create unique index if not exists idx_checks_email_token_unique on checks ((config->>'token'))
  where type = 'email' and deleted_at is null;

--bun:split

comment on index idx_checks_email_token_unique is 'Inbound email routing: a token names at most one live email check (spec 2026-11-14-01).';
