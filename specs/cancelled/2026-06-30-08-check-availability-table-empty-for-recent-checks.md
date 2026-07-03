> **CANCELLED 2026-06-30 — superseded by
> [`2026-06-30-10-check-availability-server-side-statistics-api`](../todos/2026-06-30-10-check-availability-server-side-statistics-api.md).**
> This spec was a client-side band-aid: count raw rows in the browser so young
> checks aren't blank. Spec 10 moves availability computation to a server endpoint
> backed by the shared `uptimebar` engine, which removes the client-side
> computation entirely *and* fixes the fabricated `(1 − availability) × period`
> downtime that this spec left untouched. The band-aid implementation
> (`web/dash0/src/lib/availability.ts`, `…/availability.test.ts`, and the edits to
> `availability-table.tsx`) is still uncommitted in the working tree and should be
> removed/rewritten as part of spec 10.

# Check detail "Availability" table is empty for checks younger than the raw-retention window (and the "Today" row for every check)

## Context

On the check detail page
(`http://localhost:4000/dash0/orgs/default/checks/<uid>`), the **Availability**
card shows four rows — *Today*, *Last 7 days*, *Last 30 days*, *Last 365 days* —
with columns *Availability*, *Downtime*, *Incidents*, *Longest incident*,
*Avg. incident*.

For a check created less than ~24 h ago, **every cell is empty** — Availability
and Downtime show `-`, Incidents `0`, Longest/Avg `none` — even though the check
has been probing continuously and is up.

Reproduced live on check `f10ad029-7b00-4b0c-8da4-102163404dd8` ("Google Time",
`ntp`, 10 s period, created `2026-06-30 00:12`):

- By `09:06` it had **3 084 raw results, all status `up`**, and **zero**
  `hour`/`day`/`month` aggregated rows.
- The three `ntp` checks (all created today, 00:08–00:12) were the **only**
  checks with no hourly aggregates. Every check created earlier had them. NTP is
  not special — those three are simply the only checks younger than the retention
  window.

## Root cause

Two independent design decisions combine into the bug:

1. **The aggregator intentionally lags by the raw-retention window.** The
   aggregation job keeps raw rows for `aggregation.retention_raw` **hours**
   (default **24**, `server/internal/config/config.go:313` / `:563`) before
   rolling `raw → hour` and **deleting the consumed raw rows**
   (`server/internal/jobs/jobtypes/job_aggregation.go`, boundary at `:268–290`,
   delete at `:190`). Confirmed in the live DB: the newest `hour` bucket
   (`2026-06-29 08:00`) was *inserted* at `2026-06-30 08:18` — ~24 h later — and
   the oldest surviving raw row is `2026-06-29 09:00`. So the **most recent ~24 h
   of data exists only as raw rows**, and everything older exists only as
   `hour`/`day`/`month` aggregates. The two sets are **disjoint** (aggregation
   deletes the raw it rolls up). The aggregator is healthy — 1 137 successful runs,
   latest today, next one pending — this lag is by design, not a stall.

2. **The Availability table reads aggregated tiers only and throws raw away.**
   `web/dash0/src/components/checks/availability-table.tsx`:
   - fetches `periodType: "hour,day,month"` only (`:142`);
   - `computeAvailability` **skips every raw row** (`:185–187`) under a comment
     (`:175–179`) that assumes raw "is only available for the current open
     bucket." That assumption is wrong: up to a full retention window (~24 h) of
     data is raw-only.

   Consequences:
   - A check **younger than the retention window** has no aggregates at all → the
     whole table is blank (the reported bug).
   - For **every** check, the **"Today"** row is blank/understated, because
     today's data has not been aggregated yet.

   The *Incidents / Longest / Avg* columns are correct as-is — they read the
   incidents list, and `0`/`none` is right for an all-up check.

### Same root cause, other surfaces (not fixed here — see Out of scope)

The org dashboard "24 h availability" KPI and the glance uptime strips
(`web/dash0/src/components/dashboard/dashboard-page.tsx:220–234`) query
`periodType: "day"` and `"hour"` over the last 24 h. Both are empty for the same
reason — recent data is raw-only — and the `"day"` query is doubly wrong (no
`day` aggregates exist until `hour → day` rolls up, i.e. ~30 days in), so the KPI
is effectively always empty. Related: `specs/done/.../2026-06-30-05-status-page-uptime-bar-data-source-parity.md`
and `…-02-status-page-availability-excludes-lifecycle-results.md`.

## Fix (this spec)

Make the Availability table compute recent availability from raw rows, since raw
(≤ retention) and aggregates (> retention) together cover all of history and are
disjoint.

In `web/dash0/src/components/checks/availability-table.tsx`:

1. **Fetch the recent raw window** with a second `useAllResults` query:
   `periodType: "raw"`, `periodStartAfter: startOfMinute(now) - 26h`,
   `with: "status"`, same `size`/`refetchInterval`. 26 h comfortably covers the
   24 h default retention and the full "Today" window with margin.
2. **Merge** raw rows into the period buckets alongside the aggregated rows (raw
   all falls in the last ~24 h, so it populates *Today* and the recent edge of
   the longer windows). Add the raw query to the bucketing `useMemo` deps and to
   `isLoading`.
3. **Count raw rows in `computeAvailability`**, matching the aggregator's
   semantics (`processRawResult` + the int→string status map, where
   timeout/error serialize to `"down"`):
   - `status === "up"`   → `total += 1; successful += 1`
   - `status === "down"` → `total += 1`
   - `"created"` / `"running"` / `"unknown"` → skip (lifecycle markers /
     non-measurements, mirroring `ResultStatus.IsLifecycleMarker`).

   Keep the existing aggregated branches unchanged. Because raw and aggregated
   rows are disjoint, summing them cannot double-count.
4. **Update the stale comments** (`:134–136`, `:175–179`, `:185–187`) to describe
   the raw + aggregated combination.

Outcome: a freshly-created check shows real availability immediately, and the
"Today" row reflects today's probes for every check.

### Why frontend, and the alternative

The `/results` endpoint already supports the needed filters and the table already
computes availability client-side, so this is a small, well-contained change. Raw
volume is bounded to ≤ retention (~24 h): ~1 440 rows for a default 1-minute
check; ~9 000 (paginated) for a pathological 10 s check, requesting only the
`status` field.

A more robust long-term option is a **backend availability-summary endpoint**
that combines raw + aggregates server-side in SQL — efficient (no shipping raw
rows to the browser), robust to any `retention_raw`, and reusable by the
dashboard KPI. Recorded as a follow-up; not required to fix the reported bug
under default configuration.

## Scope

- **In scope:** the check detail Availability table — *Availability* and
  *Downtime* columns becoming correct for checks younger than the retention
  window and for the *Today* row.

## Out of scope

- Dashboard "24 h availability" KPI and glance uptime strips (same root cause,
  separate follow-up).
- A backend availability-summary endpoint (the robust alternative above).
- The aggregator's retention behaviour — it is working as designed and is not
  changed.

## Testing

- **Manual (primary):** with `make dev` running, open the check page for a check
  created < 24 h ago (e.g. `f10ad029-…`). *Today / Last 7 / Last 30 / Last 365*
  should show the true availability (≈ `100%` for an all-up check) and a real
  *Downtime*, instead of `-`. Confirm an older check is unchanged and the *Today*
  row now reflects today's probes.
- **Unit:** if `computeAvailability` is extracted to a pure, exported helper,
  add a `vitest` case covering: raw-only window, aggregated-only window, a mixed
  (disjoint) window with no double-count, and that `down`/`timeout`(`"down"`)
  count as failures while `created`/`running` are ignored.
- Run `bun run lint` and `bun run build` in `web/dash0`.
