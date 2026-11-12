-- SECTION: user-contact-delivery-failure
alter table user_contacts drop column if exists delivery_failure_code;
alter table user_contacts drop column if exists delivery_failed_at;
