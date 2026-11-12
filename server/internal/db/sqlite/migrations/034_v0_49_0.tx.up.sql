-- ==========================================================================
-- SECTION: user-contact-delivery-failure (spec 2026-11-12-07)
-- ==========================================================================
-- user_contacts.delivery_failed_at / delivery_failure_code: set when a provider
-- rejects the recipient itself (Slack channel_not_found, user_not_found, ...),
-- cleared by the next successful send or a re-link. NULL means "no known
-- failure".
alter table user_contacts add column delivery_failed_at text;
alter table user_contacts add column delivery_failure_code text;
