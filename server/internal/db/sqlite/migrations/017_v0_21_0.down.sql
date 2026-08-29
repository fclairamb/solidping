-- Teardown/parity half of the consolidated v0.21.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 017_v0_21_0.up.sql.

-- ==========================================================================
-- SECTION: status-page-kiosk-token
--
-- Dropping the column revokes every outstanding kiosk token, which is the
-- correct inverse: a downgraded binary has never heard of the feature, so a
-- retained hash would be an un-revocable credential nothing can see or manage.
-- ==========================================================================

alter table status_pages drop column kiosk_token_hash;
