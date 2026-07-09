-- solidping v0.3.0 — LDAP/Active Directory bind authentication (spec
-- 2026-07-08-08, part 3). See server/internal/handlers/auth/ldap_service.go.

-- Widen the user_providers provider_type check to admit 'ldap' — same
-- technique 002_v0_2_0 used to widen user_tokens.type for oauth_refresh.
alter table user_providers drop constraint user_providers_provider_type_check;
alter table user_providers add constraint user_providers_provider_type_check
  check (provider_type in ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'discord', 'saml', 'oidc', 'ldap'));

comment on column user_providers.provider_type is 'External provider type: google, github, gitlab, microsoft, twitter, slack, discord, saml, oidc, or ldap.';
