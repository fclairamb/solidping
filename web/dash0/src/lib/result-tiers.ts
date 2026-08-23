/** Row shape `mergeResultTiers` needs — deliberately structural so this module
 * stays free of an import cycle with `@/api/hooks` (and testable on its own). */
export interface TierSortableResult {
  uid?: string;
  periodStart?: string;
}

/**
 * Merges the per-tier result pages the chart now fetches separately back into
 * the single descending sequence the old mixed `periodType=raw,hour` query
 * returned.
 *
 * The server orders results by `period_start DESC, uid DESC` (see
 * `applyResultsFilter` in both dialects), so reproducing that comparator over
 * the concatenated tiers yields exactly the row order a single query would
 * have produced — which is what lets the chart's `[...data].reverse()`,
 * `detectGaps` and pinned-result handling stay untouched by the split
 * (spec 2026-08-22-04 §1).
 *
 * Rows with no `periodStart` sort last, matching Postgres/SQLite `DESC`
 * ordering of NULLs under this query (`NULLS LAST` for DESC in Postgres);
 * in practice every result row has one.
 */
export function mergeResultTiers<T extends TierSortableResult>(
  tiers: (T[] | undefined)[],
): T[] {
  const merged: T[] = [];
  // `uid` is the results primary key and a row belongs to exactly one
  // period_type, so the same uid coming back from two tiers is always a
  // duplicate, never two distinct measurements. Dropping it keeps the merge
  // idempotent — the single mixed query could not return a row twice either.
  const seen = new Set<string>();

  for (const tier of tiers) {
    if (!tier) continue;

    for (const row of tier) {
      if (row.uid) {
        if (seen.has(row.uid)) continue;
        seen.add(row.uid);
      }

      merged.push(row);
    }
  }

  return merged.sort(compareResultsDesc);
}

function compareResultsDesc(
  a: TierSortableResult,
  b: TierSortableResult,
): number {
  const aTs = a.periodStart ? Date.parse(a.periodStart) : Number.NaN;
  const bTs = b.periodStart ? Date.parse(b.periodStart) : Number.NaN;
  const aValid = !Number.isNaN(aTs);
  const bValid = !Number.isNaN(bTs);

  if (aValid && bValid && aTs !== bTs) return bTs - aTs;
  if (aValid !== bValid) return aValid ? -1 : 1;

  // period_start ties break on uid DESC, exactly as the SQL ORDER BY does.
  const aUid = a.uid ?? "";
  const bUid = b.uid ?? "";
  if (aUid === bUid) return 0;

  return aUid < bUid ? 1 : -1;
}
