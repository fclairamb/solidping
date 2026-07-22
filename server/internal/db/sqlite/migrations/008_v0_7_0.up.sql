-- solidping v0.7.0 — see the postgres twin for the full rationale.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Custom domains for status pages (spec 2026-07-22-01)
-- ---------------------------------------------------------------------------
-- One customer-owned hostname per status page, DNS-verified (CNAME + TXT).
-- The GLOBAL partial unique index is the ownership-race anchor (unique
-- violation on write -> 409 CONFLICT), mirroring status_pages_org_slug_idx.
-- Timestamps are text (the cross-database convention used across these
-- migrations); failures is a plain integer counter.

alter table status_pages add column custom_domain text; -- Customer hostname (punycode/ASCII, lowercased). NULL = none.
alter table status_pages add column custom_domain_token text; -- base64url DNS-challenge token; TXT value is sp-domain-verify=<token>.
alter table status_pages add column custom_domain_verified_at text; -- Last passed verification (TXT+CNAME). NULL = unverified.
alter table status_pages add column custom_domain_checked_at text; -- Last periodic re-verification check.
alter table status_pages add column custom_domain_failures integer not null default 0; -- Consecutive re-verify failures; 3 clears verification.

create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;

-- ---------------------------------------------------------------------------
-- (append further v0.7.0 blocks below this line)
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- Phone (SMS/voice) contact verification (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- See the postgres twin for rationale. Timestamps are text (cross-database
-- convention used across these migrations); verify_attempts is a plain counter.
alter table user_contacts add column verify_code_hash text; -- SHA-256 hex of the pending 6-digit code; NULL when none pending.
alter table user_contacts add column verify_expires_at text; -- Pending code expiry (issue + 10 min).
alter table user_contacts add column verify_attempts integer not null default 0; -- Failed confirm attempts; 5 invalidates.

-- ---------------------------------------------------------------------------
-- Monthly SMS/voice usage counters (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- See the postgres twin. period_start is text here (cross-database convention);
-- the reserve-then-send conditional upsert keeps a burst within the cap.
create table org_usage_counters (
  organization_uid text not null,
  kind text not null,
  period_start text not null,
  count integer not null default 0,
  primary key (organization_uid, kind, period_start)
);
