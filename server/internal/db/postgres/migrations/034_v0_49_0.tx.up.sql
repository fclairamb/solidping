-- ==========================================================================
-- SECTION: user-contact-delivery-failure (spec 2026-11-12-07)
-- ==========================================================================
-- Set when a provider rejects the recipient itself (Slack channel_not_found,
-- user_not_found, ...), cleared by the next successful send or a re-link.
alter table user_contacts add column if not exists delivery_failed_at timestamptz;
alter table user_contacts add column if not exists delivery_failure_code text;

--bun:split

comment on column user_contacts.delivery_failed_at is 'When a provider last rejected this contact as an unreachable recipient. NULL when there is no known failure.';
comment on column user_contacts.delivery_failure_code is 'Provider error code of that rejection (e.g. user_not_found). NULL when delivery_failed_at is NULL.';
