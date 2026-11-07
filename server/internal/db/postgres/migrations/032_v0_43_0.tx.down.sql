-- SECTION: auto-personal-org
drop index if exists organizations_auto_personal_owner_idx;
--bun:split
alter table organizations drop column if exists auto_personal_owner_uid;
