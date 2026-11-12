-- SECTION: user-contact-delivery-failure
alter table user_contacts drop column delivery_failure_code;
alter table user_contacts drop column delivery_failed_at;
