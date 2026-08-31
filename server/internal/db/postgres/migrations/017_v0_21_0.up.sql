-- v0.21.0 — the ONE consolidated migration for the (still unreleased) v0.21.0
-- release. 016_v0_19_0 is the last RELEASED migration, so everything this
-- cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: status-page-kiosk-token       status_pages kiosk_token_hash
--   SECTION: status-page-section-selector  status_page_sections selector,
--                                          status_page_resources managed_by_selector

-- ==========================================================================
-- SECTION: status-page-kiosk-token
--
-- A wallboard/TV screen has to render a status page unattended, for months.
-- Neither existing access mode can do that: `password` issues a 12 h unlock
-- cookie (somebody re-types the password on the TV every morning), and
-- `private` 404s on every public endpoint. The kiosk token is the third
-- answer — one revocable, long-lived, per-page secret that grants READ-ONLY
-- view of exactly one page.
--
-- Stored HASHED (sha256 hex of the emitted token), never in the clear, for the
-- same reason password_hash is: the column is a credential. sha256 rather than
-- argon2id because the token is 32 bytes of CSPRNG output rather than a
-- human-chosen secret — there is no dictionary to slow down, and the TV polls
-- it every 15-30 s, which an argon2 verification per request would make
-- needlessly expensive.
--
-- NULL means "no kiosk token" — which is the correct default for every
-- existing page, and the state a revoke returns the page to.
-- ==========================================================================

alter table status_pages add column if not exists kiosk_token_hash text;

--bun:split

comment on column status_pages.kiosk_token_hash is
  'sha256 hex of the page kiosk token (spec 2026-08-29-08). NULL = no token. NEVER serialized; reads expose hasKioskToken only.';


--bun:split

-- ==========================================================================
-- SECTION: status-page-section-selector
--
-- A status page's contents were a hand-maintained list, and the failure mode
-- that matters is silent: a new service ships, its check is created, the check
-- goes down — and the page (or the wallboard inheriting its curation) stays
-- GREEN, because nobody remembered to attach it. A board that lies green is
-- worse than no board.
--
-- `selector` gives a SECTION a dynamic membership rule, deliberately at the
-- section level and not the page level, so one page can mix a hand-curated
-- "Core" section with a dynamic "Everything else". Two shapes only:
--
--     {"all": true}
--     {"labels": {"env": "prod", "public": "true"}}   -- AND, exact values
--
-- NULL — the default, and what every pre-existing section keeps — means
-- hand-curated, exactly as before. Auto-inclusion is never a default: on a
-- PUBLIC page it means a scratch check named after an internal hostname
-- reaches the internet the moment it is created, so it has to be an explicit,
-- warned-about act.
--
-- Membership is MATERIALIZED, not virtualized: the reconciler writes real
-- status_page_resources rows, marked `managed_by_selector`, because far too
-- much downstream machinery (availability enrichment, positions, the
-- badge/summary/embed endpoints, publications' affected-resource resolution)
-- assumes a real row. `managed_by_selector = false` rows are MANUAL and the
-- reconciler never touches them — that is what makes "manual placement wins"
-- a rule rather than a race.
-- ==========================================================================

alter table status_page_sections add column if not exists selector jsonb;

--bun:split

comment on column status_page_sections.selector is
  'Dynamic membership rule (spec 2026-08-29-11): {"all":true} or {"labels":{k:v,...}} (AND, exact values). NULL = hand-curated section.';

--bun:split

alter table status_page_resources
  add column if not exists managed_by_selector boolean not null default false;

--bun:split

comment on column status_page_resources.managed_by_selector is
  'TRUE when the row was materialized by the section selector and is owned by it (spec 2026-08-29-11). FALSE = manual row, never touched by the reconciler.';

--bun:split

-- The reconciler asks "which live pages in this org have a selector section?"
-- after every check write, so that lookup must not be a sequential scan of
-- every section ever created. Partial: selector sections are the rare case.
create index if not exists status_page_sections_selector_idx
  on status_page_sections (status_page_uid)
  where selector is not null and deleted_at is null;
