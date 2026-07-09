-- Rebuild user_providers with the pre-LDAP provider_type vocabulary (SQLite
-- cannot alter check constraints). Any 'ldap' rows are dropped first since
-- they can never satisfy the narrower constraint — mirrors 002_v0_2_0's
-- down migration for user_tokens.type/oauth_refresh.
delete from user_providers where provider_type = 'ldap';

create table user_providers_old (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc')),
  provider_id       text not null,
  metadata          text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now'))
);

insert into user_providers_old (uid, user_uid, provider_type, provider_id, metadata, created_at, updated_at)
select uid, user_uid, provider_type, provider_id, metadata, created_at, updated_at
from user_providers;

drop table user_providers;
alter table user_providers_old rename to user_providers;

create unique index user_providers_provider_idx on user_providers (provider_type, provider_id);
create index user_providers_user_idx on user_providers (user_uid);
