---
model: sonnet
effort: medium
---

# Check detail: region chips are ordered differently on the chart and the Recent Results filter

## Problem

On the check detail page (e.g.
`/dash0/orgs/stonal/checks/3efe4bcb-dc44-4046-8dda-a4af7d376595`), the same
set of regions is presented in a different order in the two places that show
region chips:

- **Response Times chart** — "All regions / 🇪🇺 EU1 / 🇫🇷 S3NS Paris prod"
- **Recent Results filter** — "All / 🇫🇷 S3NS Paris prod / 🇪🇺 EU1"

The two lists are derived independently and each picked a different ordering:

- The chart materializes its chip list as `Array.from(regionSet)`
  ([response-time-chart.tsx:675](web/dash0/src/components/checks/response-time-chart.tsx:675),
  set filled at
  [response-time-chart.tsx:586](web/dash0/src/components/checks/response-time-chart.tsx:586)),
  so the order is **first-seen order in the results window** — effectively
  arbitrary and can even change as new results arrive.
- The Recent Results filter uses `Array.from(set).sort()`
  ([checks.$checkUid.index.tsx:613-619](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:613)),
  i.e. **plain alphabetical by slug**. Org-relative private-region slugs start
  with `@` (e.g. `@stonaltech/s3ns-paris`), and `@` sorts before letters —
  which is why the custom "S3NS Paris prod" region lands *before* the standard
  `eu1` region there.

Same page, same regions, two orders — it reads as a bug.

## Proposal

Define a single canonical ordering and use it for both chip rows:

1. **Standard regions first** (definitions without `private: true`),
2. **then custom/private regions** (`RegionDefinition.private`, see
   [hooks.ts:132-138](web/dash0/src/api/hooks.ts:132)),
3. **alphabetical within each group**, comparing by the user-visible display
   label (`regionDisplayLabel`, i.e. `{emoji} {name}` minus the emoji — sort
   on `def.name`, falling back to the raw slug), case-insensitively.

Implementation sketch:

- Add a `sortRegionSlugs(regions: RegionDefinition[] | undefined, slugs: string[]): string[]`
  (or a comparator) next to `regionDisplayLabel` in
  [region-label.ts](web/dash0/src/lib/region-label.ts). Slugs with no matching
  definition (historical results from since-deleted regions) group with the
  custom bucket, after defined customs, alphabetically by slug.
- Use it in both places:
  - the chart's `regions` chip list
    ([response-time-chart.tsx:675](web/dash0/src/components/checks/response-time-chart.tsx:675)
    and the mirror at :696) — the chart already fetches definitions via
    `useRegions` for labels, so the data is at hand;
  - `observedRegions` on the check detail page
    ([checks.$checkUid.index.tsx:618](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:618)),
    replacing the bare `.sort()`.
- Unit-test the comparator in the existing
  [region-label.test.ts](web/dash0/src/lib/region-label.test.ts) (standard vs
  private grouping, name-based alpha ordering, unknown-slug fallback,
  `@`-prefixed slugs no longer jumping the queue).

Non-goals: reordering region lists elsewhere (check edit form, jobs pages,
pinned result box) — worth a follow-up only if their ordering also proves
inconsistent; changing which regions appear (both lists stay derived from the
observed result set).
