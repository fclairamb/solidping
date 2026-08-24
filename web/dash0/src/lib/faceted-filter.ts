// Shared helpers for the checks-list faceted filters (status, type): parsing
// a comma-separated `?status=`/`?type=` URL param into a value list, its
// inverse serializer, and the trigger-label formatting the popover button
// shows for the current selection.

/**
 * Parses a comma-separated URL param into a de-duplicated, order-preserving
 * list of trimmed lowercase tokens. Blank entries are dropped.
 *
 * Parsing is deliberately lenient: a hand-typed or stale URL with an unknown
 * token must not wedge the UI. When `allowed` is given, tokens outside that
 * set are silently dropped here (the backend still 400s an unknown *status*
 * token if one somehow reaches it — this only protects what the filter UI
 * renders as "selected"). Omit `allowed` for facets with no fixed value set
 * (e.g. check type, which is org-defined).
 */
export function parseFacetedFilterParam(
  s: string | undefined | null,
  allowed?: Set<string>,
): string[] {
  if (!s) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of s.split(",")) {
    const token = raw.trim().toLowerCase();
    if (!token || seen.has(token)) continue;
    if (allowed && !allowed.has(token)) continue;
    seen.add(token);
    out.push(token);
  }
  return out;
}

/** Inverse of parseFacetedFilterParam: joins values back into a URL param. */
export function serializeFacetedFilterParam(values: string[]): string {
  return values.join(",");
}

export interface FacetedFilterLabelStrings {
  /** Shown when nothing is selected, e.g. "All statuses". */
  all: string;
  /** Shown for 3+ selections — receives `{{count}}`, e.g. "{{count}} statuses". */
  count: (count: number) => string;
  /** Shown for exactly 2 selections — receives `{{label}}` and `{{count}}` (extra count), e.g. "{{label}} +{{count}}". */
  plusOne: (label: string, extraCount: number) => string;
}

/**
 * Computes the FacetedFilter trigger text for a selection:
 * - none selected → `all`
 * - one selected → that option's own label
 * - two selected → `plusOne(firstLabel, 1)` (e.g. "Down +1")
 * - three or more → `count(selected.length)` (e.g. "4 statuses")
 */
export function facetedFilterTriggerLabel(
  selected: string[],
  options: { value: string; label: string }[],
  strings: FacetedFilterLabelStrings,
): string {
  if (selected.length === 0) return strings.all;

  const labelOf = (value: string) =>
    options.find((o) => o.value === value)?.label ?? value;

  if (selected.length === 1) return labelOf(selected[0]);
  if (selected.length === 2) return strings.plusOne(labelOf(selected[0]), selected.length - 1);
  return strings.count(selected.length);
}
