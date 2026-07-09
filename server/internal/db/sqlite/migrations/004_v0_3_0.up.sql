-- solidping v0.3.0 — LDAP/Active Directory bind authentication (spec
-- 2026-07-08-08, part 3). See server/internal/handlers/auth/ldap_service.go.

-- Widen the user_providers provider_type check to admit 'ldap'. SQLite
-- cannot alter check constraints, so the table is rebuilt (fresh installs
-- carry no rows; the copy is for the general upgrade path) — same technique
-- 002_v0_2_0 used to widen user_tokens.type.
create table user_providers_new (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc', 'ldap')),
  provider_id       text not null,
  metadata          text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now'))
);

insert into user_providers_new (uid, user_uid, provider_type, provider_id, metadata, created_at, updated_at)
select uid, user_uid, provider_type, provider_id, metadata, created_at, updated_at
from user_providers;

drop table user_providers;
alter table user_providers_new rename to user_providers;

create unique index user_providers_provider_idx on user_providers (provider_type, provider_id);
create index user_providers_user_idx on user_providers (user_uid);
