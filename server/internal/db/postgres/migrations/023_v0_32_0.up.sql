-- v0.32.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.32.0 release. 022_v0_30_0 shipped, so this is the next free number and
-- every schema change of this cycle is appended here as a new SECTION, per
-- wiki/conventions/database.md.
--
--   SECTION: degraded-detection   the per-check degraded-detection rules, the
--                                 dry-run stamp, the status-page opt-in and the
--                                 active-degraded-incident dedup index
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 023 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: degraded-detection  (spec 2026-09-22-03)
--
-- A check that fails intermittently (7 failures in 40 minutes, each followed by
-- a success inside the confirmation period) or answers ten times slower than
-- usual opened no incident, sent no notification and left no history line. The
-- confirmation period is doing its job; what was missing is a second,
-- statistical detector beside it. One rule primitive — "M of the last N
-- countable probes match" — applied to two populations, failures and slow
-- successes.
--
-- The seven configuration columns are per check with code defaults and no
-- org-level layer. `degraded_enabled` defaults to FALSE here, which is the whole
-- rollout rule: upgrading must never start paging on its own, so every existing
-- row is off and only checks created from now on (models.NewCheck sets true)
-- open degraded incidents. Adoption comes from the dry run instead — the
-- evaluator runs for every check, and on a disabled one it opens nothing and
-- only stamps `degraded_would_fire_at`, which the check page turns into a
-- banner and the checks list into a `wouldHaveFired` filter.
--
-- `degraded_evaluated_at` is evaluator STATE, not configuration: it is the
-- oldest-evaluated-first rotation that keeps a bounded per-sweep batch from
-- starving the tail of a large install, exactly as
-- slo_alert_policies.last_evaluated_at does for burn rates.
--
-- The Go structs deliberately carry NO `default:` bun tag for any of these,
-- even though the columns have defaults — see the
-- StatusPage.AutoPublishDelaySeconds note. With `default:5` on the tag,
-- `degraded_failures: 0` would never reach the database and the failure rule
-- could not be turned off at creation time (spec 2026-08-30-04).
-- ==========================================================================

alter table checks add column if not exists degraded_failures integer not null default 5;
alter table checks add column if not exists degraded_failures_window integer not null default 60;
alter table checks add column if not exists degraded_slow integer not null default 3;
alter table checks add column if not exists degraded_slow_window integer not null default 6;
alter table checks add column if not exists slow_threshold_ms integer not null default 0;
alter table checks add column if not exists degraded_enabled boolean not null default false;
alter table checks add column if not exists degraded_would_fire_at timestamptz;
alter table checks add column if not exists degraded_evaluated_at timestamptz;

comment on column checks.degraded_failures is
  'M for the failure rule: fires when M of the last degraded_failures_window countable probes failed. 0 disables.';
comment on column checks.degraded_failures_window is
  'N for the failure rule, counted in countable probes (not seconds).';
comment on column checks.degraded_slow is
  'M for the slow rule: fires when M of the last degraded_slow_window successful probes exceeded slow_threshold_ms. 0 disables.';
comment on column checks.degraded_slow_window is
  'N for the slow rule, counted in countable probes.';
comment on column checks.slow_threshold_ms is
  'Response time above which a successful probe counts as slow, in milliseconds. 0 = the slow rule is off.';
comment on column checks.degraded_enabled is
  'Whether degraded detection may OPEN incidents on this check. FALSE = dry run (stamps degraded_would_fire_at only).';
comment on column checks.degraded_would_fire_at is
  'When the dry run last observed a degraded condition on a check that has degraded_enabled = false. NULL = never.';
comment on column checks.degraded_evaluated_at is
  'Evaluator rotation state: when the degraded sweep last evaluated this check. NULL = never.';

--bun:split

-- A degraded incident is an internal operations signal about intermittence, not
-- a customer-facing outage, so it is NOT auto-published. Opt in per page — the
-- same shape as auto_publish itself, and deliberately a separate flag: a page
-- that announces outages has not thereby agreed to announce "3 of the last 6
-- probes were slow".
alter table status_pages add column if not exists publish_degraded boolean not null default false;

comment on column status_pages.publish_degraded is
  'Whether degraded incidents auto-publish to this page. FALSE = only check outages do.';

--bun:split

-- Dedup enforced by the database, not merely by the evaluator: at most ONE open
-- degraded incident per check. Two evaluator replicas racing on the same minute
-- both see "no open incident" and both insert; this is what makes the loser fail
-- instead of double-notifying. Mirrors uq_active_slo_burn_incident.
create unique index if not exists uq_active_degraded_incident
  on incidents (check_uid)
  where state = 1 and kind = 'degraded' and deleted_at is null;

--bun:split

-- The evaluator's work queue reads enabled, live checks oldest-evaluated first.
-- Without this it is a full scan of `checks` every minute.
create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled;
