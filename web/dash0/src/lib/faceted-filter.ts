// Shared helpers for the checks-list faceted filters (status, type): parsing
// a comma-separated `?status=`/`?type=` URL param into a value list, its
// inverse serializer, and the value badges the popover trigger shows for the
// current selection.

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

/**
 * Value badges a FacetedFilter trigger shows next to its dimension name:
 * - nothing selected (or only values with no matching option) → no badge
 * - one or two selected → each option's own label
 * - three or more → a single `selectedLabel(count)` badge (e.g. "3 selected")
 *
 * Selected values that match no option are ignored: a stale URL token must not
 * render a badge for a value the popover cannot show ticked.
 */
export function facetedFilterBadges(
  selected: string[],
  options: { value: string; label: string }[],
  selectedLabel: (count: number) => string,
): string[] {
  const labels: string[] = [];
  for (const value of selected) {
    const option = options.find((o) => o.value === value);
    if (option) labels.push(option.label);
  }
  if (labels.length <= 2) return labels;
  return [selectedLabel(labels.length)];
}
