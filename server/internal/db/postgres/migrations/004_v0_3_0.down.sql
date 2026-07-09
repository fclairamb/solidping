-- Revert user_providers.provider_type to the pre-LDAP vocabulary. Any 'ldap'
-- rows are dropped first since they can never satisfy the narrower
-- constraint — mirrors 002_v0_2_0's down migration for
-- user_tokens.type/oauth_refresh.
delete from user_providers where provider_type = 'ldap';

alter table user_providers drop constraint user_providers_provider_type_check;
alter table user_providers add constraint user_providers_provider_type_check
  check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc'));

comment on column user_providers.provider_type is 'External provider: google, github, gitlab, microsoft, twitter, slack, saml, oidc.';
