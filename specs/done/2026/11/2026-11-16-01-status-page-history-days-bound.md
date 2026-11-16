---
model: opus
effort: medium
---

# A status page's `historyDays` is stored unbounded, and every public view allocates and loops once per day

## Problem

Found by the security scan of 2026-11-16 (finding AO2). Severity: Medium (availability, the AA1/AE1 class). An org admin sets the value. On SaaS any authenticated user can create an org and become its admin. After that, every view of the public status page triggers the cost. The viewer needs no account, and the admin's own visits trigger it too.

- **Write, no bound.** The legacy `historyDays` input is still accepted next to the `historyPeriod` enum:
  - create stores the raw int (`server/internal/handlers/statuspages/service.go:1194-1196`);
  - update copies it into the row (`service.go:1544`) and derives only the enum from it (`:1564-1566`);
  - the MCP tools pass any int through (`server/internal/mcp/tools_statuspages.go:224-226`, `:316-318`);
  - the column is a plain `integer not null default 90`, with no CHECK (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:687`).

  `models.PeriodFromDays` maps every value other than 7 and 30 to `90d` (`server/internal/db/models/status_page.go:45-54`). So a huge value gives a "90d" daily page whose `history_days` column is still the huge number.
- **Read, no bound.** `enrichWithAvailability` uses `page.HistoryDays` as is:
  - for the window start (`service.go:3105`);
  - as the bucket count passed to `uptimebar.BucketAvailability` (`:3110`);
  - in `buildAvailabilityData` (`:4084-4090`), which runs once per resource. It calls `make([]AvailabilityPoint, 0, historyDays)`, then loops `historyDays` times with two `time.Format` calls per iteration.
- **Impact:** with a value near the int32 maximum, the first allocation asks for well over 100 GB, and the API process is OOM-killed. That takes down every tenant on the replica. The row survives restarts, so the next view kills the process again. A value around a million costs tens of MB and millions of iterations per resource on every uncached view.
- The response-time half of this same page was already hard-capped for this reason (`service.go:3303-3309`, spec 2026-09-21-03 A.1). The availability half was missed. Negative values also reach `make` with a negative capacity, which panics.

## Proposal

1. Validate on write, in REST create/update and in both MCP tools. When `historyPeriod` is absent, accept `historyDays` only in `1..90`, or only the values `PeriodFromDays` knows (1, 7, 30, 90), and return a `VALIDATION_ERROR` otherwise. When the enum is present, ignore `historyDays` as today.
2. Clamp on read, as defence in depth for rows already stored: derive the day count from `pagePeriod(page)` with `daysForPeriod`, not from the raw column, wherever `enrichWithAvailability` and `buildAvailabilityData` size their windows and slices. At minimum, clamp `page.HistoryDays` to `[1, 90]` before `AddDate`, `BucketAvailability` and `make`.
3. Apply the same clamp to the `ListPublicStatusUpdates` window (`loadRecentUpdates`, `service.go:2716-2725`). A huge day count overflows the Postgres interval and returns a 500 on the public page.
4. Add `minimum`/`maximum` to `historyDays` in `server/internal/app/openapi/openapi.yaml` (`:15454`).
5. Add a migration (the next free `NNN`, both dialects) that rewrites stored values outside `[1, 90]` with `daysForPeriod(history_period)`.

## Tests

- `server/internal/handlers/statuspages` service test: creating and updating a page with `historyDays` of `0`, `-1`, `91` and `2000000000` (no `historyPeriod`) is refused with a validation error. This fails before the fix. Positive control: `historyDays: 30` is accepted and stored as `30d`.
- Read-path test: a page row forced to `history_days = 2000000000` (written straight to the database) renders its public view with exactly 90 daily points per resource, and no allocation proportional to the column. This fails or hangs before the fix. Positive control: a normal 30d page renders 30 points.
- MCP tool test: `create_status_page` / `update_status_page` with an out-of-range `historyDays` returns an error.
- Migration test (both dialects): an out-of-range row is rewritten, and an in-range row is untouched.
