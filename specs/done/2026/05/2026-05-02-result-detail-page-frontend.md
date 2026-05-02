# Result detail page — dash0 frontend

## Context

Companion spec to `2026-05-02-result-detail-endpoint-backend.md`. Once the backend exposes `GET /api/v1/orgs/:org/checks/:check/results/:uid` (with covering-aggregation fallback), dash0 needs a page to consume it and rows from the existing "Recent results" table need to link to it.

Today, on `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:756`, the recent-results table renders `<TableRow>` per result with no click handler. The `OrgResult` type at `web/dash0/src/api/hooks.ts:101` has all the fields we need to render except the new `fallback` envelope.

Target URL: `/dash0/orgs/$org/checks/$checkUid/results/$resultUid`.

## Scope

In scope:
- New TanStack Router file `checks.$checkUid.results.$resultUid.tsx` rendering the detail page.
- New `useResult` hook in `api/hooks.ts`, plus an extension of the `OrgResult` type for the optional `fallback` object.
- Fallback banner UI when the response carries `fallback`.
- Make the existing recent-results table rows on the check detail page clickable, navigating to the new route.
- i18n keys under the existing `checks` namespace (the page lives within the checks tree).

Out of scope:
- Linking from the AvailabilityTable cells (each cell is one aggregation row; doable later but requires plumbing the underlying row's uid through the table component — keep this spec focused).
- Linking from the response-time chart points.
- Any pagination/navigation between results (next/prev) — defer.
- Public/unauthenticated access. The detail page lives under `/orgs/$org/...` which is auth-gated by the existing `__root.tsx` / org route layout.

## Type updates — `web/dash0/src/api/hooks.ts`

After the existing `OrgResult` interface (line 119), add:

```ts
export interface ResultFallbackInfo {
  requestedUid: string;
  requestedAt: string; // RFC3339
  reason: "rolled_up_to_hour" | "rolled_up_to_day" | "rolled_up_to_month";
}

export interface OrgResultDetail extends OrgResult {
  fallback?: ResultFallbackInfo;
}
```

Add a hook (place it next to `useResults` around line 410):

```ts
export function useResult(
  org: string,
  checkUid: string,
  resultUid: string,
) {
  return useQuery<OrgResultDetail>({
    queryKey: ["result", org, checkUid, resultUid],
    queryFn: () =>
      apiFetch<OrgResultDetail>(
        `/api/v1/orgs/${org}/checks/${checkUid}/results/${resultUid}`,
      ),
    enabled: !!org && !!checkUid && !!resultUid,
    // Results are immutable once written. No refetch interval.
    staleTime: Infinity,
  });
}
```

(`apiFetch` is the existing wrapper used by other hooks in the same file — match its signature exactly. `staleTime: Infinity` because once a result is fetched it won't change; the only reason to refetch is the user manually retrying.)

## New route — `web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx`

File-based route resolves to `/orgs/$org/checks/$checkUid/results/$resultUid` (confirmed by reading the existing `checks.$checkUid.edit.tsx` pattern).

```ts
export const Route = createFileRoute(
  "/orgs/$org/checks/$checkUid/results/$resultUid"
)({
  component: ResultDetailPage,
});
```

### Page structure

Top to bottom:

1. **Back button** to `/orgs/$org/checks/$checkUid` (matches the back-arrow pattern from `checks.$checkUid.index.tsx:391`).

2. **Title row**: `Result {uid.slice(0, 8)}` + status badge (color = green/red/yellow/grey, same logic as line 377) + period type badge (`raw`/`hour`/`day`/`month`).

3. **Fallback banner** (only when `data.fallback` is present). Use a yellow/amber alert (re-use the `<Alert>` shadcn component if present in `web/dash0/src/components/ui/`; if not, use a plain `bg-yellow-500/10 border border-yellow-500/30 rounded-md p-4` block — there are precedents in the codebase, grep for `bg-yellow-500/10`):

   > **The exact result you requested is no longer stored.**
   > It was rolled up into this **{hour|day|month} aggregation** ({periodStart}–{periodEnd}) covering its timestamp ({fallback.requestedAt}). The original UID `{fallback.requestedUid}` was deleted as part of automatic aggregation.

   Make the banner unmistakable — it changes the meaning of every value below it (the user sees aggregate stats, not their specific event). Don't make it dismissible.

4. **Period card**: periodStart (always), periodEnd (if set), periodType, region (if set), worker (raw only — link out only if we have a worker view; otherwise plain text), duration (raw: `durationMs`; aggregated: `min / max / p95`).

5. **Aggregation stats card** (only when `periodType !== "raw"`): totalChecks, successfulChecks, availabilityPct (formatted as `99.95 %`).

6. **Metrics card**: key/value table from `data.metrics` (object). Format numeric values via the same `_min`/`_max`/`_avg`/`_pct`/`_rte`/`_sum`/`_cnt`/`_val` suffix convention used by the aggregation job (`server/internal/jobs/jobtypes/job_aggregation.go:376`). For `_pct` show as `xx.xx %`; for `_min`/`_max`/`_avg` add unit `ms` if the metric name contains `duration` or `latency`; otherwise raw number with 2 decimals. Keep this as a small `formatMetric(name, value)` helper local to the file — no separate util module.

7. **Output card**: same key/value rendering as the lastResult output block already in `checks.$checkUid.index.tsx:717`. Reuse the styling. Make it `max-h-96 overflow-auto` because outputs can be large.

8. **Footer link**: "View all results for this check →" linking to `/orgs/$org/checks/$checkUid` (a sibling spec idea would be a results-list filtered view, but for now point back at the parent).

### Loading / error states

- `isLoading`: skeletons matching the card layout, similar to `checks.$checkUid.index.tsx:341`.
- 404 (`RESULT_NOT_FOUND`): centered "Result not found — it may have been deleted as part of automatic aggregation, and we couldn't find an aggregation covering its timestamp." plus a "Back to check" button. Use `<QueryErrorView>` (`@/components/shared/error-views`) — it already handles error code → message; pass `resource={t("results:title")}`.
- 401: handled globally by the existing API error interceptor, redirects to login (per `docs/conventions/frontend-errors.md`).

## Linking from the recent-results table

In `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` around line 766 (`results.data.map((result) => (<TableRow key={result.uid}>...`):

Wrap each row in a `<Link>` from `@tanstack/react-router`, or — to keep table-row semantics intact — set `onClick` on the row plus `cursor-pointer hover:bg-muted/50` classes, and call `navigate({ to: "/orgs/$org/checks/$checkUid/results/$resultUid", params: { org, checkUid, resultUid: result.uid! } })`.

Pick the `onClick` approach (TanStack's `<Link>` doesn't render cleanly inside `<tr>`). Add a guard: skip navigation if `result.uid` is missing — the type allows `undefined`. Add `data-testid={`result-row-${result.uid}`}` for future Playwright tests.

## i18n

Add keys under `web/dash0/src/locales/{en,fr,de,es}/checks.json` (or wherever the `checks` namespace files live — grep for an existing key like `"detail.recentResults"` to find them):

```jsonc
"resultDetail": {
  "title": "Result",
  "back": "Back to check",
  "period": "Period",
  "region": "Region",
  "worker": "Worker",
  "duration": "Duration",
  "durationMin": "Min", "durationMax": "Max", "durationP95": "P95",
  "totalChecks": "Total checks",
  "successfulChecks": "Successful checks",
  "availability": "Availability",
  "metrics": "Metrics",
  "output": "Output",
  "viewAll": "View all results for this check",
  "fallback": {
    "title": "The exact result you requested is no longer stored.",
    "rolledUpToHour": "It was rolled up into this hour aggregation ({{start}}–{{end}}) covering its timestamp ({{ts}}).",
    "rolledUpToDay":  "It was rolled up into this day aggregation ({{start}}–{{end}}) covering its timestamp ({{ts}}).",
    "rolledUpToMonth":"It was rolled up into this month aggregation ({{start}}–{{end}}) covering its timestamp ({{ts}}).",
    "originalUid": "The original UID {{uid}} was deleted as part of automatic aggregation."
  },
  "notFound": "Result not found — it may have been deleted as part of automatic aggregation, and we couldn't find an aggregation covering its timestamp."
}
```

Follow whatever the project's locale-update convention is (grep one of the recent specs `2026-05-02-status0-translate-availability-bar-and-chart.md` for the exact format if needed). All four languages must land together — partial translations break the locale fallback chain.

## Verification

1. `make build-dash0` — TypeScript compiles, no new lint warnings.
2. `make dev-test` already running on `:4000`. Smoke:
   - Navigate to `/dash0/orgs/test/checks/<some-check>` → click a recent-result row → arrives at `/dash0/orgs/test/checks/<check>/results/<uid>`. Page renders status, period, metrics, output. URL is bookmarkable (refresh works).
   - Force a fallback: pick a real hourly aggregation for a check, hand-craft a UUIDv7 whose embedded timestamp falls inside that hour (the aggregation job source has the timestamp encoding; for a smoke test, easier to wait for a raw result to be rolled up — `make dev-test` triggers aggregation often enough — then load the raw URL again). Banner appears. Page still renders sensibly with the aggregated values.
   - Force a 404: visit `/dash0/orgs/test/checks/<check>/results/not-a-uuid`. Error view appears, back button works.
3. Switch language via `?lang=fr` (if dash0 supports it — status0 does; check whether dash0 has the same `i18n.language` plumbing). Banner copy switches.
4. Refresh the page on a fallback URL. The banner is still there (the fallback is server-side, deterministic — repeat requests return the same fallback target).

## Risks worth knowing about

- The fallback URL is **not** pinned: the same UID might map to an `hour` aggregation today and a `day` aggregation in two months once hourly rows are also rolled up. That's fine, but a bookmarked URL's *meaning* drifts toward coarser aggregation over time. The banner copy must always reflect the current level, not a cached one — already handled because we render straight from the response.
- If the aggregation job runs *between* the user clicking the row and the detail page rendering, you can get a 200 with `fallback` even when the user expected a direct hit. Acceptable; the banner explains.

## Final grep check

After landing:

```bash
rtk grep -rn "useResult\b" web/dash0/src/   # at least 2 hits: the hook + the page
rtk grep -rn "rolled_up_to_" web/dash0/src/ # banner translations + reason switch
```

---

## Implementation Plan

1. Extend `OrgResult` typing with optional `fallback` info via a new `OrgResultDetail` type; add `useResult` hook in `api/hooks.ts` calling the new backend endpoint.
2. Create `web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx` rendering: back button, status/title, fallback banner (when present), period card, aggregation stats card (when not raw), metrics card, output card, footer link.
3. Make rows in the recent-results table on the check detail page clickable via `onClick` + `cursor-pointer hover:bg-muted/50`, navigating to the new route.
4. Add `resultDetail` locale subtree to `en/fr/de/es` checks.json with all the strings used by the page and banner.
5. `make build-dash0 lint-dash` clean.
