-- Teardown/parity only — never run in production.
-- Reverses 011_user_integration_identities.up.sql.

drop index if exists user_integration_identities_organization_idx;

--bun:split

drop index if exists user_integration_identities_integration_external_idx;

--bun:split

drop index if exists user_integration_identities_integration_user_idx;

--bun:split

drop table if exists user_integration_identities;
