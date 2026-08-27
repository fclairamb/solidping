-- Teardown/parity half of the consolidated v0.19.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 016_v0_19_0.up.sql.

-- ==========================================================================
-- SECTION: entitlement-source-split
--
-- Maps org-admin back to admin. This is the CORRECT inverse rather than a
-- lossy approximation: a downgraded binary has never heard of 'org-admin', so
-- it would resolve such a row through merge's default branch and PlanWeight's
-- default branch — i.e. demote the org to the free tier and drop its stored
-- limits' provenance. Old code reads 'admin' as "paid, null-filled, billing
-- may overwrite", which is precisely what an org-admin row means.
--
-- Lossy in one direction only: rows a superadmin created through the new
-- editor are folded back in with the rest, since the distinction they encode
-- does not exist in the schema being downgraded to.
-- ==========================================================================

update org_entitlements
set payload    = jsonb_set(payload, '{source}', '"admin"'),
    updated_at = now()
where payload->>'source' = 'org-admin';
