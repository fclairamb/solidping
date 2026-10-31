-- SECTION: auto-personal-org (spec 2026-10-30-04)
-- organizations.auto_personal_owner_uid: the user this org was created for
-- automatically at their first sign-in; NULL for orgs created by hand. The
-- partial unique index keeps one live personal org per user. No foreign key:
-- SQLite cannot drop a column a foreign key names (down migration).
alter table organizations add column auto_personal_owner_uid text;
create unique index organizations_auto_personal_owner_idx on organizations (auto_personal_owner_uid) where deleted_at is null and auto_personal_owner_uid is not null;
