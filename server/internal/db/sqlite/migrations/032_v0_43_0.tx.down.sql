-- SECTION: auto-personal-org
drop index if exists organizations_auto_personal_owner_idx;
alter table organizations drop column auto_personal_owner_uid;
