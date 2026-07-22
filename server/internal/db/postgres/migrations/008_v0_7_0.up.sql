-- solidping v0.7.0 — consolidated release migration.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Custom domains for status pages (spec 2026-07-22-01)
-- ---------------------------------------------------------------------------
-- A status page can be served on a customer-owned hostname
-- (e.g. status.acme.com), DNS-verified via a CNAME (routing) plus a TXT
-- challenge (ownership). One domain per page. The GLOBAL partial unique index
-- is the ownership-race anchor: it stops one org claiming another org's live
-- domain, and a unique violation on write maps to 409 CONFLICT — mirroring
-- status_pages_org_slug_idx from 001_v0_1_0.up.sql:694.

alter table status_pages add column custom_domain varchar(253);
alter table status_pages add column custom_domain_token varchar(64);
alter table status_pages add column custom_domain_verified_at timestamptz;
alter table status_pages add column custom_domain_checked_at timestamptz;
alter table status_pages add column custom_domain_failures smallint not null default 0;

create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;

comment on column status_pages.custom_domain is
  'Customer-owned hostname (punycode/ASCII, lowercased) the page is served on. NULL = none. Globally unique among live rows.';
comment on column status_pages.custom_domain_token is
  'Opaque DNS-challenge token (base64url). Set while a domain is configured; embedded in the _solidping-challenge TXT record as sp-domain-verify=<token>.';
comment on column status_pages.custom_domain_verified_at is
  'When the domain last passed verification (TXT + CNAME). NULL = unverified; only verified+enabled+public pages are served on the custom host.';
comment on column status_pages.custom_domain_checked_at is
  'When the periodic re-verification job last checked this domain.';
comment on column status_pages.custom_domain_failures is
  'Consecutive re-verification failures. At 3 the verification is cleared (domain release/takeover protection).';

-- ---------------------------------------------------------------------------
-- (append further v0.7.0 blocks below this line)
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- Phone (SMS/voice) contact verification (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- Per-user phone contacts are created unverified. A 6-digit code (SHA-256
-- hashed, 10-min expiry, attempt-capped) is issued via the org's Twilio
-- connection and confirmed here; only a verified number is ever texted/dialed
-- by the escalation dispatcher.
alter table user_contacts add column verify_code_hash varchar(64);
alter table user_contacts add column verify_expires_at timestamptz;
alter table user_contacts add column verify_attempts smallint not null default 0;

comment on column user_contacts.verify_code_hash is
  'SHA-256 hex of the in-flight 6-digit verification code. NULL when no verification is pending or after a successful confirm.';
comment on column user_contacts.verify_expires_at is
  'Expiry of the in-flight verification code (issue + 10 minutes).';
comment on column user_contacts.verify_attempts is
  'Failed confirm attempts for the in-flight code; at 5 the code is invalidated.';
