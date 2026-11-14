-- SECTION: email-token-unique
-- The re-minted tokens are not restored: the duplicates they replaced were the
-- defect.
drop index if exists idx_checks_email_token_unique;
create index if not exists idx_checks_email_token on checks ((config->>'token'))
  where type = 'email' and deleted_at is null;
