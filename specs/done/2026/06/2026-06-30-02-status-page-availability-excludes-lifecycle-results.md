# Status page availability reads <100% for healthy checks — exclude lifecycle results

## Context

The public status page (status0) shows an availability percentage like **98.305%** for a
check that has had **zero real downtime**, while the dashboard and the badges endpoint
show **100.000%** for the same check and window:

- Status page: `http://localhost:4000/status0/default/time-servers`
- Badge (correct): `http://localhost:4000/dash0/orgs/default/badges?check=ntp-google&components=status%2Cavailability%2Cduration%2Cresponse-time%2Cuptime-bar%2Cresponse-time-graph&period=24h`

In the reported screenshot the *only* red segment is **today** ("Aujourd'hui"); every prior
day is grey (noData) or healthy. That is the tell: **today's bucket is computed
differently from every other surface**, and it drags the weighted overall down to ~98%.

Root cause: there are **three** availability-calculation code paths and one of them counts
`created`/`running` *lifecycle* rows in its denominator. Lifecycle rows (a check that was
just scheduled / is mid-flight) are not measurements — every other path skips them. The
status page's raw-synthesis path does not, so each in-flight/just-created raw row counts as
"not up" and pulls the percentage below 100%.

This spec **unifies the three paths behind one shared rule** so the status page matches the
badges endpoint and the aggregation job — exactly the "use the same calculation method"
the request asks for. It is backend-only.

---

## Current state (verified against source)

The status enum (`server/internal/db/models/result.go:13–31`): `created`=1, `running`=2,
`up`=3, `down`=4, `timeout`=5, `error`=6, `degraded`=7, `warning`=8.

| Path | Location | Skips `created`/`running`? | Counts as "up" |
|---|---|---|---|
| **Aggregation job** (authoritative — writes the stored `availability_pct` on `hour`/`day`/`month` rows) | `server/internal/jobs/jobtypes/job_aggregation.go:725–728` and `752–755` | **Yes** — *"Skip non-data statuses (initial, running) — they are lifecycle markers, not measurements"* | `up` **and** `warning` (Decision A, line 748–754) |
| **Badges** (the surface the request trusts) | `server/internal/handlers/badges/service.go:782–809` (`calculateAvailability`) | **Yes** — `continue` on created/running (lines 792–794) | `up` only |
| **Status page raw→daily synthesis** | `server/internal/handlers/statuspages/service.go:1111–1155` (`aggregateRawToDaily`) | **NO** — `total++` for every non-nil status (lines 1124–1133) | `up` only |

### Why only *today* is wrong

`enrichWithAvailability` (`statuspages/service.go:877–978`) reads **stored** daily rows
(`period_type="day"`) for past days — those were produced by the aggregation job, which
*excludes* lifecycle rows, so past days are correct. Only **today** has no stored daily row
yet (the raw→hour→day rollups lag), so it is synthesized from raw via
`fillTodayFromRaw` (lines 1050–1106) → `aggregateRawToDaily` (lines 1111–1155). That
synthesis is the one buggy path, so the error is isolated to today's bucket — matching the
screenshot.

`buildAvailabilityData` (lines 1198–1260) then computes the overall as a
`TotalChecks`-weighted average across buckets (lines 1240–1252); today's deflated bucket
pulls the headline number down. For a brand-new check whose only populated bucket is today,
overall == today, so the full ~1.7% error surfaces directly.

### Why excluding lifecycle rows is sufficient to reach 100%

The check has no real `down`/`timeout`/`error` rows in the window (that is what "should be
100%" means). With those absent, the *only* non-`up` raw rows are `created`/`running`.
Removing them from the denominator yields 100% — for both the status page's calendar-day
synthesis and the badge's rolling-24h count. (The calendar-day vs rolling-24h window
difference is real but immaterial when there is zero genuine downtime.)

---

## Requirements

1. The status page's synthesized **today** bucket must compute availability with the **same
   rule as the aggregation job**: exclude `created`(1)/`running`(2) from the denominator;
   count `up`(3) and `warning`(8) toward success.
2. A check with no genuine downtime reads **100%** on the status page — overall percentage,
   today's per-day status colour, and the daily bar — matching the badge and the dashboard.
3. The three calculation paths are routed through **one shared helper** so they cannot drift
   apart again. This is the substance of "use the same calculation method".
4. No regression to stored aggregates: the aggregation job's numbers are unchanged (it
   already implements the target rule; it only adopts the shared helper).

---

## Design decision — which rule is canonical?

> **Q — Match "badges" (up only) or the "aggregation job" (up + warning)?**
> The request points at the badge, but the badge and the aggregation job differ on one
> point: the job counts `warning` as up, the badge does not. The status page must be
> **self-consistent** — today's synthesized bucket sits directly next to past buckets that
> were written by the aggregation job. If the status page matched the badge (up only) while
> past days came from the job (up + warning), today and yesterday would disagree whenever a
> `warning` exists.
>
> **Decision:** adopt the **aggregation-job** semantics everywhere (exclude lifecycle; `up`
> + `warning` = success) and route all three paths through it. This fixes the reported bug
> (created/running is the dominant error and both rules agree on excluding it) *and* makes
> the status page internally consistent. The only behavioural change beyond the bug fix is
> that **badges now count `warning` as up** — a rare status, and aligning it removes the
> last source of cross-surface drift. Logged as a deliberate change in the risk log.

---

## Recommended implementation — one shared availability rule

### 1. Shared predicates on `ResultStatus` — `server/internal/db/models/result.go`

Add next to `StatusToString` (line 43), so every caller already importing `models` can use
them:

```go
// IsLifecycleMarker reports whether the status is a non-measurement lifecycle state
// (created/running) that must be excluded from availability denominators.
func (s ResultStatus) IsLifecycleMarker() bool {
	return s == ResultStatusCreated || s == ResultStatusRunning
}

// CountsAsUp reports whether a raw result counts toward availability success.
// Warning is "up with something to report" (the target is reachable), so it counts.
func (s ResultStatus) CountsAsUp() bool {
	return s == ResultStatusUp || s == ResultStatusWarning
}

// RawAvailability computes (successCount, countableTotal) over raw results, skipping
// lifecycle markers. Callers derive pct = 100*success/total when total > 0.
func RawAvailability(results []*Result) (success, total int) {
	for _, r := range results {
		if r.Status == nil {
			continue
		}
		s := ResultStatus(*r.Status)
		if s.IsLifecycleMarker() {
			continue
		}
		total++
		if s.CountsAsUp() {
			success++
		}
	}
	return success, total
}
```

### 2. Status page — `server/internal/handlers/statuspages/service.go`

Rewrite the counting loop in `aggregateRawToDaily` (lines 1119–1140) to use the shared
helper, preserving the existing duration-of-successful-rows behaviour:

```go
success, total := models.RawAvailability(rawResults)
if total == 0 {
	return nil
}
avail := 100.0 * float64(success) / float64(total)
// ... duration averaged over up rows as today (now via ResultStatus.CountsAsUp) ...
```

The duration accumulation (lines 1129–1132) keeps averaging over up rows — switch its
`*rawResult.Status == int(models.ResultStatusUp)` guard to
`ResultStatus(*rawResult.Status).CountsAsUp()` so a warning row's duration is included,
consistent with it counting as up.

### 3. Aggregation job — `server/internal/jobs/jobtypes/job_aggregation.go`

`accumulateRawResult` (lines 725–758) already implements this rule inline. Replace the
hand-written `created/running` skip (725–728) and the `up || warning` success check
(752–754) with calls to `IsLifecycleMarker()` / `CountsAsUp()`. **No behavioural change** —
this is dedup so the rule lives in one place and the linter/tests cover one implementation.

### 4. Badges — `server/internal/handlers/badges/service.go`

Route `calculateAvailability` (lines 782–809) through `models.RawAvailability`. This keeps
the existing lifecycle skip and makes `warning` count as up (the one intended change; see
the decision above).

---

## Out of scope

- The 24h period option — separate spec `2026-06-30-03-status-page-24h-history-period.md`,
  which **depends on this one** so its new hourly buckets use the unified rule.
- Changing the calendar-day vs rolling-window anchoring of the status page (immaterial to
  the reported bug; would be a behaviour change of its own).
- The `availabilityToStatus` thresholds (`statuspages/service.go:1292–1301`) and badge
  colour thresholds — unchanged; once the percentage is right they classify correctly.
- Any frontend change — the status0 page already renders whatever the API returns.

---

## Verification

```bash
make dev-test   # backend + dash0 + status0, SP_RUNMODE=test, port 4000
```

- **Unit (`statuspages/service_test.go`):** feed `aggregateRawToDaily` a raw set of, say,
  18 `up` + 2 `created` + 1 `running` and assert `AvailabilityPct == 100` and
  `TotalChecks == 18` (lifecycle rows excluded from both). Add an `up`+`warning` case → still
  100%. Add a real `down` → reflects the genuine miss.
- **Unit (`models` package):** table test `RawAvailability` / `IsLifecycleMarker` /
  `CountsAsUp` across all eight statuses.
- **Regression:** existing `job_aggregation` tests must pass unchanged (the job's output is
  identical); existing badge tests adjusted only for the `warning`-counts-as-up case.
- **End-to-end sanity:** for a healthy check, the status page API and the badge must agree:
  ```bash
  TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
    -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
    'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
  # Status page overall for the check should read ~100, matching:
  curl -s 'http://localhost:4000/api/v1/status-pages/default/time-servers' \
    | jq '.sections[].resources[] | {name, overall: .availability.overallAvailabilityPct}'
  ```
  Confirm the previously-red "today" segment is now green and the headline reads 100.000%.
- `make lint` — no new findings; never relax `.golangci.yml` ([[feedback_lint_strict]]).
- Treat any intermittent test failure as a bug to root-cause ([[feedback_flaky_tests_are_bugs]]).

---

## Key files

| File | Change |
|---|---|
| `server/internal/db/models/result.go` | **+** `ResultStatus.IsLifecycleMarker()`, `.CountsAsUp()`, `RawAvailability([]*Result)` |
| `server/internal/handlers/statuspages/service.go` | **~** `aggregateRawToDaily` uses `RawAvailability` (the fix) + `CountsAsUp` duration guard |
| `server/internal/jobs/jobtypes/job_aggregation.go` | **~** `accumulateRawResult` uses the shared predicates (no behaviour change) |
| `server/internal/handlers/badges/service.go` | **~** `calculateAvailability` uses `RawAvailability` (warning now counts as up) |
| `server/internal/handlers/statuspages/service_test.go` | **~** lifecycle-exclusion + warning cases |
| `server/internal/db/models/result_test.go` | **+** predicate table tests |

---

## Risk log

| Risk | Mitigation |
|---|---|
| Badges behaviour change: `warning` now counts as up. | Intended (cross-surface consistency); `warning` is rare. Call out in the PR; adjust the one badge test that asserts the old number. |
| A status truly should *not* count as up (future status added). | Centralising in `CountsAsUp()` makes the policy explicit and one-line to audit, vs. three drifting copies today. |
| Stored daily aggregates still hold old values for past days. | None needed — past stored rows were already computed with the correct rule; only the live-synthesis path was wrong. No backfill required. |

**Status**: Todo | **Created**: 2026-06-30 | **Depended on by**: `2026-06-30-03-status-page-24h-history-period.md`

---

## Implementation Plan

1. **Shared rule** — add `IsLifecycleMarker()`, `CountsAsUp()`, `RawAvailability()` to
   `models/result.go`; table-test all eight statuses.
2. **Fix the status page** — route `aggregateRawToDaily` through `RawAvailability`; switch
   the duration guard to `CountsAsUp()`. Unit-test the lifecycle-exclusion case → 100%.
3. **Dedup the aggregation job** — replace the inline skip/success checks in
   `accumulateRawResult` with the predicates; confirm its tests pass byte-for-byte.
4. **Align badges** — route `calculateAvailability` through `RawAvailability`; update the
   one warning-related assertion.
5. **End-to-end check** — `make dev-test`, confirm the status page overall matches the
   `period=24h` badge (100% for a healthy check, red "today" segment gone).
6. **QA** — `make test` + `make lint`; no new lint findings.
