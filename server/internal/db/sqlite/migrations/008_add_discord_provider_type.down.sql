create table organization_providers_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('slack', 'google', 'github', 'gitlab', 'microsoft', 'saml', 'oidc')),
  provider_id       text not null,
  provider_name     text,
  metadata          text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

insert into organization_providers_new (uid, organization_uid, provider_type, provider_id, provider_name, metadata, created_at, updated_at, deleted_at)
  select uid, organization_uid, provider_type, provider_id, provider_name, metadata, created_at, updated_at, deleted_at from organization_providers
  where provider_type != 'discord';

drop table organization_providers;
alter table organization_providers_new rename to organization_providers;

create unique index idx_org_providers_type_id on organization_providers (provider_type, provider_id) where deleted_at is null;
create index idx_org_providers_org on organization_providers (organization_uid) where deleted_at is null;

create table user_providers_new (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade,
  provider_type     text not null check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'saml', 'oidc')),
  provider_id       text not null,
  metadata          text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now'))
);

insert into user_providers_new (uid, user_uid, provider_type, provider_id, metadata, created_at, updated_at)
  select uid, user_uid, provider_type, provider_id, metadata, created_at, updated_at from user_providers
  where provider_type != 'discord';

drop table user_providers;
alter table user_providers_new rename to user_providers;

create unique index user_providers_provider_idx on user_providers (provider_type, provider_id);
create index user_providers_user_idx on user_providers (user_uid);
