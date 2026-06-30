# Status pages: add a 24h (hourly) history period alongside 7d / 30d / 90d

## Context

Status pages today offer a **history period of 7, 30, or 90 days**. The request is to also
allow **24h** — the last 24 hours shown as **24 hourly buckets**, exactly like the badges
uptime-bar's `period=24h`
(`http://localhost:4000/dash0/orgs/default/badges?...&period=24h`).

24h is not "1 day". The status page renders **daily** buckets and stores the window as
`history_days` — an integer count of days. A 24h view needs **hourly** granularity (24
segments of 1h, hour labels, hourly synthesis of the current incomplete hour). So this is
not a new dropdown value over the existing machinery; it adds an **hourly bucketing mode**
to the status page, parallel to the daily mode.

The badges endpoint already solved the same problem and is the model to mirror:
`uptimeBarPeriodInfo` (`server/internal/handlers/badges/service.go:509–522`) maps
`"24h" → (period_type=hour, count=24, bucket=1h)` and `"7d"/"30d"/"90d" → (day, N, 24h)`.

> **Depends on `2026-06-30-02-status-page-availability-excludes-lifecycle-results.md`.** The
> new hourly "current hour" bucket is synthesised from raw the same way today's daily bucket
> is, so it must use the unified availability rule from spec 02 to read 100% for a healthy
> check. Land 02 first.

---

## Current state (verified against source)

| Concern | Location | Today |
|---|---|---|
| Period selector (admin) | `web/dash0/src/components/shared/status-page-form.tsx:201–216` | `<Select>` with `7` / `30` / `90` → `historyDays:number` (state line 61, default 90) |
| Data model | `server/internal/db/models/status_page.go:21` | `HistoryDays int bun:"history_days,notnull,default:90"` |
| API request/response | `server/internal/handlers/statuspages/service.go` — `StatusPageResponse.HistoryDays` (≈93), create/update `HistoryDays *int` (≈167, 181) | integer day-count only |
| Backend enrichment | `statuspages/service.go:877–978` (`enrichWithAvailability`) | always `period_type="day"`, `HistoryDays` back; synth daily from hourly (`synthesizeMissingDailyBuckets`) then raw (`fillTodayFromRaw`) |
| Bucket response shape | `statuspages/service.go` — `ResourceAvailabilityData` + `DailyAvailabilityPoint{date,availabilityPct,status}` (≈133–145) | per-**day** points keyed by date string |
| Public render — bar | `web/status0/src/components/shared/availability-bar.tsx` (label `t("daysAgo",{count:historyDays})` line 62; `getBarColor` 10–14) | N daily segments + "X days ago" label |
| Public render — view | `web/status0/src/components/shared/status-page-view.tsx` (overall `toFixed(3)` lines 88–91; passes `historyDays` line 243) | renders `page.historyDays` |
| TS types | `web/status0/src/api/hooks.ts:73` and `web/dash0/src/api/hooks.ts:1109,1145,1157` | `historyDays:number` |
| Badges 24h (reuse the model) | `badges/service.go:509–522` `uptimeBarPeriodInfo` | `24h→(hour,24,1h)`; `7d/30d/90d→(day,N,24h)` |

The public status0 page has **no** period switcher of its own; it renders the single window
configured on the page. So "add 24h" means **add a `24h` option to the admin selector** and
teach the backend + public renderer the hourly mode.

---

## Design decision — how to represent 24h in the model

`history_days int` cannot express "24 hours / hourly". Two viable options:

> **Option A (recommended) — explicit period enum.** Add `history_period TEXT NOT NULL
> DEFAULT '90d'` with values `24h | 7d | 30d | 90d`, mirroring the badge vocabulary the user
> already knows. Backfill from the existing column (`7→7d`, `30→30d`, `90→90d`; any other
> `N→{N}d`). The enum becomes the source of truth and drives bucketing via a shared
> `statusPagePeriodInfo(period) → (periodType, count, bucketDuration)` helper (the analogue
> of `uptimeBarPeriodInfo`). `history_days` is kept populated for one release for
> backward-compat, then removed. **Pros:** self-describing, matches badges, trivially
> extensible to `1h`/`6h`/`1y` later, no magic sentinel. **Cons:** a column + migration on both
> Postgres and SQLite.
>
> **Option B (no migration) — `history_days = 0` sentinel.** Reuse the existing column;
> `0` means "24h / hourly", `>0` keeps today's daily meaning. Form maps `24h↔0`. **Pros:**
> no schema change. **Cons:** a magic value that any `historyDays >= 1` validation or future
> reader can mis-handle; not self-describing.

**Recommendation: Option A.** It matches the period vocabulary the request itself uses, keeps
the badge and status-page period models aligned, and avoids a sentinel that will bite a
future reader. The rest of this spec is written against Option A; the Option-B fallback is
noted inline where it diverges. Migrations go in **both** `internal/db/postgres/migrations/`
and `internal/db/sqlite/migrations/` per the consolidated-per-release convention — beware
stale pre-consolidation dev DBs silently skipping new migrations
([[project_migration_consolidation_stale_db]]); reset the dev DB or apply the delta.

---

## Recommended implementation

### Backend

1. **Model** — `server/internal/db/models/status_page.go`: add
   `HistoryPeriod string bun:"history_period,notnull,default:'90d'"`. Add a `StatusPagePeriod`
   type with the four constants and a `Valid()` check. Migration adds the column + backfill
   `UPDATE status_pages SET history_period = CASE history_days WHEN 7 THEN '7d' WHEN 30 THEN
   '30d' ELSE '90d' END` (both DB dialects).

2. **Period → buckets helper** — in `statuspages/service.go`, add
   `statusPagePeriodInfo(period) (periodType string, count int, bucket time.Duration)`
   mirroring `uptimeBarPeriodInfo`: `24h→("hour",24,time.Hour)`, `7d→("day",7,24h)`,
   `30d→("day",30,24h)`, `90d→("day",90,24h)`.

3. **Generalise `enrichWithAvailability`** (lines 877–978) from "daily, HistoryDays back" to
   "(periodType, count, bucket) back":
   - Fetch stored rows of `periodType` over the window.
   - **24h path:** fetch `period_type="hour"` for the last 24 hours; synthesise the current
     (incomplete) hour from raw — the hourly analogue of `fillTodayFromRaw`/
     `aggregateRawToDaily` (reuse `models.RawAvailability` from spec 02). Build 24 hourly
     buckets anchored on `now.Truncate(time.Hour)` going back 23 hours.
   - **7/30/90d path:** unchanged daily logic, now selected via the helper.
   - Generalise `synthesizeMissingDailyBuckets` / `aggregateRawToDaily` to a bucket duration
     parameter (rename to `synthesizeMissingBuckets` / `aggregateRawToBucket`), or add an
     hourly sibling — prefer parameterising so the two modes share one code path.

4. **Response shape** — rename `DailyAvailabilityPoint` → `AvailabilityPoint` with both a
   `date` (kept for back-compat) and the bucket's RFC3339 `time`, plus carry the active
   `period`/`bucketUnit` (`"day"|"hour"`) on `ResourceAvailabilityData` (or top-level
   `StatusPageResponse`) so the frontend labels correctly. Keep `availabilityPct` + `status`.
   The weighted-overall in `buildAvailabilityData` (1198–1260) is unit-agnostic and stays.

5. **Create/update + validation** — accept `historyPeriod` on the create/update DTOs
   (`statuspages/service.go` ≈167/181 and the handler), validate against the enum
   (`VALIDATION_ERROR` on a bad value), keep accepting `historyDays` for one release mapped
   to the enum.

### Frontend — dash0 admin (`web/dash0/`)

Per the repo design-reference rule, build from existing primitives
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`).

- `status-page-form.tsx`: change the `<Select>` (201–216) to operate on the **period enum**
  (`"24h"|"7d"|"30d"|"90d"`) instead of the int; add `<SelectItem value="24h">24 hours</SelectItem>`
  above the day options. State `historyDays` (line 61) → `historyPeriod`; submit payload
  (line 71) sends `historyPeriod`.
- `api/hooks.ts` (1109/1145/1157): add `historyPeriod: StatusPagePeriod` to the StatusPage
  type and create/update inputs.
- `status-pages.new.tsx` (≈31) and `status-pages.$statusPageUid.edit.tsx` (≈72): pass
  `historyPeriod` through.

### Frontend — status0 public (`web/status0/`)

- `api/hooks.ts:73`: add `historyPeriod` (and the bucket unit field) to the `StatusPage` /
  response types.
- `availability-bar.tsx`: when the unit is `hour`, render 24 hourly segments and switch the
  left label from `t("daysAgo",{count})` (line 62) to `t("hoursAgo",{count:24})`; tooltip
  formats the bucket's time as an hour. Daily mode unchanged.
- `status-page-view.tsx`: pass the period/unit through (it already passes `historyDays` at
  line 243); overall `toFixed(3)` formatting unchanged.

### i18n

- dash0 `locales/{en,fr,de,es}/statusPages.json`: add the "24 hours" option label next to
  the existing `historyDays` key (line 35).
- status0 `locales/{en,fr,de,es}/*`: add a `hoursAgo` key (`"{{count}}h ago"` /
  `"il y a {{count}} h"` / …) alongside the existing `daysAgo`.

---

## Out of scope

- The availability **calculation correctness** — owned by spec 02 (this spec consumes its
  shared rule for the current-hour synthesis).
- A per-visitor period switcher on the public status page (24h vs 7d toggled by the reader).
  This spec adds 24h as a **page-level configured** option, consistent with how 7/30/90 work
  today. A public toggle can be a later spec.
- New badge periods or any badge change — badges already support 24h.
- Sub-hour (`1h`, `6h`) or `1y` periods — the enum is designed to allow them later, but they
  are not in scope.

---

## Verification

```bash
make dev-test   # backend + dash0 + status0, port 4000
```

- **Migration:** `make migrate` on a fresh DB and on a populated one; confirm existing pages
  backfill (`90→'90d'`, etc.) and new pages default to `90d`. Verify on **both** SQLite and
  Postgres ([[project_migration_consolidation_stale_db]]).
- **Admin:** edit a status page → the History Period select shows **24 hours / 7 / 30 / 90
  days**; pick 24 hours, save, reload → persists.
- **Public (status0):** a page set to 24h renders **24 hourly segments** with an
  "24h ago → now" axis; a healthy check reads **100.000%** (depends on spec 02). Switch the
  page to 7d → daily segments return. Verify on a mobile width (responsive, all pages must
  work on mobile per repo conventions).
- **API:**
  ```bash
  curl -s 'http://localhost:4000/api/v1/status-pages/default/<slug>' \
    | jq '{historyPeriod, bucketUnit: .sections[0].resources[0].availability}'
  ```
  24h → 24 points with hourly `time`s; 30d → 30 daily points.
- **Unit (`statuspages/service_test.go`):** table-test `statusPagePeriodInfo`; test the
  hourly enrichment builds 24 buckets and synthesises the current hour from raw via the
  shared rule.
- **E2E (`web/dash0/e2e/`):** extend the status-pages spec to set 24h and assert it
  round-trips; prefer Playwright over Chrome MCP ([[feedback_browser_testing]]). Treat any
  flake as a bug ([[feedback_flaky_tests_are_bugs]]).
- `make test`, `make lint` (no new findings — never relax config [[feedback_lint_strict]]),
  `make test-dash`.

---

## Key files

| File | Change |
|---|---|
| `server/internal/db/models/status_page.go` | **+** `HistoryPeriod` field + `StatusPagePeriod` type/constants/`Valid()` |
| `server/internal/db/postgres/migrations/*`, `server/internal/db/sqlite/migrations/*` | **+** add `history_period` column + backfill (both dialects) |
| `server/internal/handlers/statuspages/service.go` | **~** `statusPagePeriodInfo`; generalise `enrichWithAvailability` + bucket synthesis to hourly/daily; rename `DailyAvailabilityPoint`→`AvailabilityPoint` (+`time`,`bucketUnit`); accept/validate `historyPeriod` |
| `server/internal/handlers/statuspages/handler.go` | **~** parse/validate `historyPeriod` on create/update |
| `web/dash0/src/components/shared/status-page-form.tsx` | **~** Select operates on the period enum; add `24h` option |
| `web/dash0/src/api/hooks.ts`, `web/dash0/src/routes/orgs/$org/status-pages.new.tsx`, `…status-pages.$statusPageUid.edit.tsx` | **~** thread `historyPeriod` |
| `web/status0/src/api/hooks.ts` | **~** add `historyPeriod` + bucket unit to types |
| `web/status0/src/components/shared/availability-bar.tsx`, `…/status-page-view.tsx` | **~** hourly segments + `hoursAgo` label when unit is hour |
| `web/dash0/src/locales/{en,fr,de,es}/statusPages.json`, `web/status0/src/locales/{en,fr,de,es}/*` | **~/+** "24 hours" option; `hoursAgo` key |
| `web/dash0/e2e/`, `server/internal/handlers/statuspages/service_test.go` | **~** 24h round-trip + hourly enrichment tests |

---

## Risk log

| Risk | Mitigation |
|---|---|
| Migration on both Postgres **and** SQLite; stale pre-consolidation dev DBs skip it. | Add to both dialects' consolidated release migration; reset/patch the dev DB per [[project_migration_consolidation_stale_db]]; verify backfill in `make dev-test`. |
| Renaming `DailyAvailabilityPoint` breaks the status0 client. | Ship the response rename and the status0 type change together; keep `date` on the point for back-compat; the API is internal to this repo. |
| Current-hour bucket is empty early in the hour (raw→hour rollup lag). | Synthesise from raw (hourly analogue of `fillTodayFromRaw`); if still empty, render `noData` (grey), never red. |
| Dropping `history_days` too early breaks an in-flight client. | Keep `history_days` populated and accepted for one release; remove in a follow-up once `history_period` is everywhere. |
| 24h reads <100% for a healthy check if spec 02 hasn't landed. | Hard dependency on spec 02; land it first (the current-hour synthesis shares that rule). |

**Status**: Todo | **Created**: 2026-06-30 | **Depends on**: `2026-06-30-02-status-page-availability-excludes-lifecycle-results.md`

---

## Implementation Plan

1. **Land spec 02 first** — the shared `models.RawAvailability` rule the current-hour
   synthesis relies on.
2. **Model + migration** — add `history_period` (+ `StatusPagePeriod` type) and the backfill
   on Postgres and SQLite; `make migrate` on fresh and populated DBs.
3. **Period helper** — `statusPagePeriodInfo(period)` mirroring `uptimeBarPeriodInfo`.
4. **Backend enrichment** — parameterise `enrichWithAvailability` + the synthesis helpers by
   bucket unit; add the 24h hourly path (stored hourly rows + raw current-hour synthesis);
   generalise the response point to carry `time` + `bucketUnit`. Unit-test both modes.
5. **Create/update + validation** — accept and validate `historyPeriod`; keep mapping
   `historyDays` for one release.
6. **dash0 admin** — Select on the enum + `24 hours` option; thread `historyPeriod` through
   form, hooks, new/edit routes; register nothing new in the design reference unless a new
   primitive is introduced (none expected).
7. **status0 public** — hourly segments + `hoursAgo` label/tooltip when the unit is hour;
   thread the period/unit through; verify mobile.
8. **i18n** — `24 hours` (dash0) and `hoursAgo` (status0) across en/fr/de/es.
9. **QA** — `make test`, `make lint`, `make test-dash`; E2E round-trip of the 24h setting;
   confirm a healthy 24h page reads 100.000% and the bar shows 24 hourly segments.

## Implementation Plan (execution log)

Option A (explicit period enum) chosen, per spec recommendation. Spec 02 dependency
(`models.RawAvailability`) is already merged on this batch branch.

1. **Model + migration** — `models.StatusPage.HistoryPeriod string` (`history_period`,
   notnull default `'90d'`); `StatusPagePeriod` type with `StatusPagePeriod24h/7d/30d/90d`
   constants + `Valid()`; add `HistoryPeriod` to `StatusPageUpdate`. New consolidated
   migration `004_status_page_period.{up,down}.sql` in **both** postgres and sqlite dirs:
   `ADD COLUMN history_period` + backfill `CASE history_days WHEN 7 '7d' WHEN 30 '30d' ELSE
   '90d'`. Set `HistoryPeriod` in `NewStatusPage`. DB `UpdateStatusPage` (pg+sqlite) handles
   the field.
2. **Period helper** — `statusPagePeriodInfo(period) (periodType string, count int, bucket
   time.Duration)` in `statuspages/service.go`, mirroring `uptimeBarPeriodInfo`:
   `24h→(hour,24,1h)`, `7d→(day,7,24h)`, `30d→(day,30,24h)`, default/`90d`→(day,90,24h).
3. **Enrichment** — branch `enrichWithAvailability` on `period.PeriodType()`. Daily path
   unchanged (existing helpers). New `enrichHourly` path: fetch stored `hour` rows for last
   24h, synthesise the current incomplete hour from raw (reuse `aggregateRawToHour`, the
   hourly analogue of `aggregateRawToDaily` already sharing `models.RawAvailability`), build
   24 buckets anchored on `now.Truncate(time.Hour)` going back 23h via
   `buildHourlyAvailabilityData`. Add `AvailabilityPoint{date,time,availabilityPct,status}` +
   `Period`/`BucketUnit` on `ResourceAvailabilityData` (keep `DailyAvailability` populated for
   back-compat; add `bucketUnit`/`period`).
4. **Response + create/update + validation** — `StatusPageResponse.HistoryPeriod`; create/update
   DTOs accept `historyPeriod` (still accept `historyDays`, mapped); validate enum in handler →
   `VALIDATION_ERROR`. `convertPageToResponse` emits it.
5. **dash0** — form Select on the enum (`24h|7d|30d|90d`, "24 hours" option first); state
   `historyPeriod`; thread through hooks types + new/edit routes (send `historyPeriod`).
6. **status0** — types gain `historyPeriod`/`bucketUnit`/`AvailabilityPoint.time`;
   `availability-bar.tsx` renders hourly segments + `hoursAgo` label/tooltip when
   `bucketUnit==='hour'`; thread period/unit through `status-page-view.tsx`.
7. **i18n** — dash0 `form.historyPeriod24h`; status0 `hoursAgo` across en/fr/de/es.
8. **Tests** — `statusPagePeriodInfo` table test; `aggregateRawToHour` parity with
   `aggregateRawToDaily`/badges rule; `buildHourlyAvailabilityData` builds 24 buckets with
   correct boundaries + current-hour synthesis. E2E: extend dash0 `status-pages.spec.ts` to
   round-trip the 24h setting (author; may not run locally if devloop isn't `SP_RUNMODE=test`).
