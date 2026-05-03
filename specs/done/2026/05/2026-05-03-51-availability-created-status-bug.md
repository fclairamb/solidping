# Availability calculation must exclude "created" lifecycle results

## Problem

A check's life starts with an automatically-inserted `Result` row whose `status = ResultStatusCreated (1)`. This row marks *when* the check was registered — it is not a measurement. The same applies to the transient `ResultStatusRunning (2)` status.

For results `[created, up, up, up]`, the per-check availability table on `dash0` shows **75% (3/4)**. It should show **100% (3/3)**.

The bug is **frontend-only and self-healing after one hour**:

- For a brand-new check, no hourly aggregate exists yet, so the availability table falls back to a client-side `rawAvailability()` calculation over raw results — which divides by the unfiltered row count.
- Once the aggregation job produces the first hourly row, the table switches to `avgAvailability()` (which consumes the server's already-correct `availabilityPct`) and the displayed number becomes correct.

The user-visible symptom: a freshly created, healthy check appears to have ~75–80% availability for its first hour, then silently jumps to 100%.

## Root cause

`web/dash0/src/components/checks/availability-table.tsx:175-189`:

```typescript
function rawAvailability(
  data: { status?: string; periodStart?: string }[] | undefined,
  start: Date,
  end: Date
): number | null {
  if (!data?.length) return null;
  const inWindow = data.filter((r) => {
    if (!r.periodStart) return true;
    const t = new Date(r.periodStart);
    return t >= start && t <= end;
  });
  if (inWindow.length === 0) return null;
  const successCount = inWindow.filter((r) => r.status === "up").length;
  return (successCount * 100) / inWindow.length;
}
```

`inWindow` includes every raw row in the time range — including the lifecycle `created` and transient `running` rows. They inflate the denominator without contributing to the numerator.

## Reference: backend already does this correctly

The backend has the canonical filter in `server/internal/handlers/badges/service.go:170-172`:

```go
if status == models.ResultStatusCreated || status == models.ResultStatusRunning {
    continue
}
```

And the aggregation job (`server/internal/jobs/jobtypes/job_aggregation.go:697-700`) early-returns *before* incrementing `totalChecks` for created/running results. This is verified by `TestAggregateMetrics_NonDataStatuses` in `server/internal/jobs/jobtypes/job_aggregation_test.go:370-426`, which feeds a mix of 5 results (2 UP + 1 DOWN + 1 RUNNING + 1 INITIAL) and asserts `totalChecks == 3`, `successfulChecks == 2`, `availabilityPct ≈ 66.67%`.

The frontend fallback should match this behavior.

## Fix

Edit `web/dash0/src/components/checks/availability-table.tsx`. Filter `created` and `running` out of both numerator and denominator:

```typescript
function rawAvailability(
  data: { status?: string; periodStart?: string }[] | undefined,
  start: Date,
  end: Date
): number | null {
  if (!data?.length) return null;
  const inWindow = data.filter((r) => {
    if (!r.periodStart) return true;
    const t = new Date(r.periodStart);
    return t >= start && t <= end;
  });
  const measured = inWindow.filter(
    (r) => r.status !== "created" && r.status !== "running"
  );
  if (measured.length === 0) return null;
  const successCount = measured.filter((r) => r.status === "up").length;
  return (successCount * 100) / measured.length;
}
```

Implementation notes:

- Return `null` (not `0`) when no *measured* row remains in the window. This matches the existing "no data" semantics — the table cell renders `—` instead of a misleading `0%`. Important for very-fresh checks whose only row is `created`.
- Keep the two-element status check inline. No need to extract a `MEASURED_STATUSES` constant unless one already exists for reuse elsewhere.

## Suggested test

If a sibling test file exists for `availability-table`, add cases for `rawAvailability` (extracting it into an exportable helper if needed):

| Input statuses (all in window)         | Expected |
|----------------------------------------|----------|
| `[created, up, up, up]`                | 100      |
| `[created, running]`                   | null     |
| `[created, up, down]`                  | 50       |
| `[]` / undefined                       | null     |
| All rows out of window                 | null     |
| `[up, down, timeout, error]`           | 25       |

If extracting the function feels disproportionate for this fix, skip the unit test and verify manually per the plan below.

## Out of scope (do NOT change)

- **`server/internal/jobs/jobtypes/job_aggregation.go`** — `processRawResult` already excludes created/running. Has test coverage.
- **`server/internal/handlers/badges/service.go`** — `calculateAvailability` already filters. Don't duplicate logic elsewhere.
- **`/api/v1/orgs/{org}/results` API** — do **not** filter `created` rows out of the API response. The check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:296`) reads `lastResult.status === "created"` to drive fast-poll behavior, and the results table at line 851 renders the `created` badge intentionally.
- **`web/dash0/src/components/dashboard/dashboard-page.tsx` `weightedAvailability`** — consumes server-computed `totalChecks`/`successfulChecks` which are correct.
- **`web/dash0/src/components/checks/availability-table.tsx` `avgAvailability`** — consumes server-computed `availabilityPct` which is correct.

## Verification

1. Start the stack: `make dev-test`.
2. Log in (test-mode creds: `test@test.com` / `test` / org `test`).
3. Create a new HTTP check, e.g. `https://example.com`, period `30s`.
4. Open the check detail page **immediately**. The "Today" row in the availability table should:
   - **Before fix**: show ~75–80% while the `created` row is still in `rawResults`.
   - **After fix**: show `100%` (or `—` if no executed run yet).
5. Wait through several check periods — the value stays at 100% while the check is up.
6. After ~1 hour (when the hourly aggregation runs), the table switches to `avgAvailability` and continues to display 100%.
7. `cd web/dash0 && bun run lint && bun run build`.
8. Smoke test the backend: `make test` (no backend changes expected).

## Files

- **Edit**: `web/dash0/src/components/checks/availability-table.tsx` (lines 175-189)
- **Reference (read, do not edit)**:
  - `server/internal/handlers/badges/service.go:160-186`
  - `server/internal/jobs/jobtypes/job_aggregation.go:686-732`
  - `server/internal/jobs/jobtypes/job_aggregation_test.go:370-426`
  - `server/internal/db/models/result.go:13-25`
