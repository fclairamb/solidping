-- Teardown/parity half of the consolidated v0.21.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 017_v0_21_0.up.sql.

-- ==========================================================================
-- SECTION: status-page-section-selector
--
-- Dropping the columns turns every selector section back into a plain
-- hand-curated one. The materialized rows are deliberately LEFT IN PLACE: a
-- downgraded binary has never heard of selectors, and silently emptying a
-- customer's status page is a far worse outcome than leaving a snapshot of
-- what the selector had last resolved to. They simply become manual rows.
-- ==========================================================================

drop index if exists status_page_sections_selector_idx;

--bun:split

alter table status_page_resources drop column if exists managed_by_selector;

--bun:split

alter table status_page_sections drop column if exists selector;

--bun:split

-- ==========================================================================
-- SECTION: status-page-kiosk-token
--
-- Dropping the column revokes every outstanding kiosk token, which is the
-- correct inverse: a downgraded binary has never heard of the feature, so a
-- retained hash would be an un-revocable credential nothing can see or manage.
-- ==========================================================================

alter table status_pages drop column if exists kiosk_token_hash;
