-- Teardown/parity half of the consolidated v0.19.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 016_v0_19_0.up.sql.

-- ==========================================================================
-- SECTION: entitlement-source-split
--
-- Maps org-admin back to admin. The correct inverse, not an approximation: a
-- downgraded binary has never heard of 'org-admin' and would resolve such a
-- row through the default branch, demoting the org to the free tier. Old code
-- reads 'admin' as "paid, null-filled, billing may overwrite", which is what
-- an org-admin row means.
--
-- Lossy in one direction only: rows a superadmin created through the new
-- editor fold back in with the rest, since that distinction does not exist in
-- the schema being downgraded to.
-- ==========================================================================

update org_entitlements
set payload    = json_set(payload, '$.source', 'admin'),
    updated_at = datetime('now')
where json_extract(payload, '$.source') = 'org-admin';
