-- ==========================================================================
-- SECTION: auto-personal-org (spec 2026-10-30-04)
-- ==========================================================================
-- The user an organization was created for automatically at their first
-- sign-in. Marks the org as an auto-created personal org (the abandoned-org
-- cleanup and the colleague hint target it), and the partial unique index
-- makes the creation race-safe: one live personal org per user.
alter table organizations add column if not exists auto_personal_owner_uid uuid references users(uid) on delete set null;

--bun:split

create unique index if not exists organizations_auto_personal_owner_idx on organizations (auto_personal_owner_uid) where deleted_at is null and auto_personal_owner_uid is not null;

--bun:split

comment on column organizations.auto_personal_owner_uid is 'User this organization was created for automatically at their first sign-in (auth.auto_personal_org). NULL for organizations created by hand. Abandoned ones (no check, one member) are deleted after 30 days.';
