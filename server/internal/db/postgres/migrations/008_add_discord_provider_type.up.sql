alter table organization_providers drop constraint organization_providers_provider_type_check;
alter table organization_providers add constraint organization_providers_provider_type_check
  check (provider_type in ('slack', 'google', 'github', 'gitlab', 'microsoft', 'discord', 'saml', 'oidc'));

alter table user_providers drop constraint user_providers_provider_type_check;
alter table user_providers add constraint user_providers_provider_type_check
  check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc'));

comment on column organization_providers.provider_type is 'External provider type: slack, google, github, gitlab, microsoft, discord, saml, oidc.';
