---
model: sonnet
effort: high
---

# The checks list can't filter on Validating, multiple statuses, or check type

## Problem

The checks list (`/dash0/orgs/$org/checks`) has a status filter, but it is
missing half the picture:

1. **`validating` cannot be selected — or even requested.** The status
   `Select` at
   [checks.index.tsx:1486-1507](web/dash0/src/routes/orgs/$org/checks.index.tsx#L1486)
   offers only All / Up / Down / Pending (`created`), while the list itself
   renders `validating` and `warning` badges and the group headers count them
   ([checks.index.tsx:300-308](web/dash0/src/routes/orgs/$org/checks.index.tsx#L300)).
   Worse, hand-writing `?status=validating` in the URL returns **400**: the
   backend's `parseStatusFilter`
   ([handler.go:32-57](server/internal/handlers/checks/handler.go#L32))
   accepts `up,down,created,degraded,warning` but not `validating` — an
   omission, since `CheckStatusValidating = 5` is a real stored value of
   `checks.status` ([check.go:21-25](server/internal/db/models/check.go#L21))
   and the filter is a plain `IN` over that column, so nothing structural
   prevents it. The doc comment at
   [handler.go:229](server/internal/handlers/checks/handler.go#L229) is also
   stale (`up,down,error,timeout,created` — `error`/`timeout` were never
   accepted tokens).

2. **Multi-status works in the plumbing but is unreachable and un-renderable.**
   `parseStatusFilter` already splits on commas, and the frontend passes the
   raw `?status=` string straight through (`validateSearch` keeps it a string
   at [checks.index.tsx:161](web/dash0/src/routes/orgs/$org/checks.index.tsx#L161),
   `useInfiniteChecks` forwards it at
   [checks.index.tsx:1099](web/dash0/src/routes/orgs/$org/checks.index.tsx#L1099)).
   So `?status=down,created` filters correctly today — but the `Select` bound
   to `statusParam ?? "all"` has no item for the composite value, so the
   trigger renders as if no filter were active. `?status=down,validating`,
   the case the user actually wants ("everything unhealthy or about to be"),
   400s because of (1).

3. **No check-type filter.** The backend has supported comma-separated
   `?type=` since the tunnel selector
   ([handler.go:106-110](server/internal/handlers/checks/handler.go#L106),
   `ListChecksOptions.Types` at
   [service.go:853](server/internal/handlers/checks/service.go#L853)), and
   `useChecks` even documents the option — but the list page never exposes
   it, and `useInfiniteChecks`
   ([hooks.ts:643-655](web/dash0/src/api/hooks.ts#L643)) doesn't accept a
   `type` option at all.

Incidentally, the status `Select`'s labels ("All statuses", "Up", "Down",
"Pending") are hard-coded English in a page that is otherwise fully
translated.

## Proposal

### Backend (small)

- Add `validating` → `models.CheckStatusValidating` to `parseStatusFilter`,
  and fix the stale comment to the real token list
  (`up,down,created,validating,degraded,warning`).
- Handler/service tests: `?status=validating` returns only validating checks
  (with an up check present as the **negative control**);
  `?status=down,validating` returns the union; an unknown token still 400s
  with `VALIDATION_ERROR`. Cover both dialects if the existing status-filter
  tests do.

### Frontend — status filter becomes a checkbox popover

Replace the single-value status `Select` with a small **faceted filter**: a
popover trigger + checkbox list, one row per status. This is the answer to
"select a status and then optionally another" — one click opens it, each
click toggles a status, and it degrades gracefully to the single-status case.
The page already ships the exact interaction precedent:
[`LabelFilter`](web/dash0/src/components/shared/label-filter.tsx) is a
popover filter sitting in the same toolbar, and `popover.tsx` + `checkbox.tsx`
exist in `components/ui/`.

- Trigger text reflects the selection: "All statuses" (none), the status name
  (one), "N statuses" or "Down +1" (several). Keep the
  `data-testid="status-filter"`.
- Options: `up`, `down`, `validating`, `warning`, `created` (labelled
  Pending) — the statuses the list actually renders. Whether to also list
  `degraded` (aggregated-only, never produced live — see
  [check.go:26-30](server/internal/db/models/check.go#L26)) is the
  implementer's call; the URL token keeps working either way.
- URL is the source of truth, per the existing convention: serialize the
  selection to a comma-joined `?status=` (unset when empty), `replace: true`
  like today's writer. Parse leniently on read (split, trim, drop unknown
  tokens) so a hand-typed URL with an unknown token doesn't wedge the UI —
  the backend will still 400 it, and the list error state covers that.
- A cold deep link `?status=down,validating` must render with both checkboxes
  ticked and the trigger label correct on first paint. Read the param
  directly from `Route.useSearch()` with no local-state mirror (like
  `groupBy`) — URL-seeded state on this layout route has regressed before.
- `isFiltering`
  ([checks.index.tsx:1048-1052](web/dash0/src/routes/orgs/$org/checks.index.tsx#L1048))
  already keys off `statusParam`; keep it truthy for any non-empty selection.

Add the new faceted-filter primitive to
[`design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx)
— that page is the canonical catalog and this is a new reusable pattern.

### Frontend — type filter

Same faceted-filter component, second instance, for check type →
`?type=http,dns` (singular param name, comma-separated, per the API
convention — the backend needs zero changes).

- Options come from `useCheckTypes(org)`
  ([hooks.ts:3923](web/dash0/src/api/hooks.ts#L3923)) so the list matches
  what the org can actually create; reuse the type labels the check form
  uses rather than raw slugs.
- Add `type?: string` to `useInfiniteChecks`' options and confirm
  `buildChecksUrl` serializes it (it already does for `useChecks`).
- Extend `validateSearch` and the `isFiltering` computation with the new
  param; every `navigate()` writer must carry the other params forward
  (they already use the functional `(prev) => ({...prev, ...})` form —
  keep it that way).

### i18n

All new labels (status names, "All statuses", "All types", the "N selected"
trigger format) go through i18n in **all four locales** (`en`, `fr`, `de`,
`es` under `web/dash0/src/locales/`), and the currently hard-coded English
strings in this toolbar get moved to keys as part of the change. Run
`bun run test:unit` — a missing locale key is a failure, not a follow-up.

### Tests (E2E, `web/dash0/e2e/`)

- Toggle two statuses in the popover → URL carries `?status=a,b`, rows narrow
  accordingly, and a check in a third status is **absent** (negative
  control), not just "fewer rows".
- Cold-load a `?status=down,validating` deep link → filter UI shows both
  selected, list is filtered.
- Type filter alone, and combined with a status filter — neither clears the
  other, and combining narrows further.
- A `validating` check appears under `?status=validating` (requires seeding
  or driving a check into the validating state via the confirmation period —
  the test-mode fixtures may need one).

## Out of scope

- Multi-select for the labels filter (unchanged).
- Persisting filter presets.
- The incidents-list check filter (spec 2026-08-24-03).

## Implementation Plan

1. **Backend**: `parseStatusFilter` gains `validating` → `models.CheckStatusValidating`;
   fix stale doc comment. Add `list_status_filter_test.go` covering
   `?status=validating` (negative control: up check present but excluded),
   `?status=down,validating` (union), unknown token → 400 VALIDATION_ERROR.
2. **Faceted filter primitive**: new `web/dash0/src/components/shared/faceted-filter.tsx`
   (popover trigger + checkbox list), generic over `{value, label}` options,
   modeled on `label-filter.tsx` + `popover.tsx` + `checkbox.tsx`. Add to
   design-reference.tsx.
3. **Status filter**: replace the `Select` in `checks.index.tsx` with the new
   faceted filter. `validateSearch` keeps `status` as a lenient comma-string;
   parse to a `Set`/array via `useMemo` off `Route.useSearch()` directly (no
   local mirror). Writer uses functional `(prev) => ({...prev, status: ...})`,
   `replace: true`, unset when empty. Keep `data-testid="status-filter"`.
   Options: up/down/validating/warning/created(Pending); degraded excluded
   (aggregated-only, never live) — URL token still parses fine either way
   since the backend accepts it.
4. **Type filter**: add `type?: string` to `useInfiniteChecks`, confirm
   `buildChecksUrl` serialization. Add second faceted-filter instance sourced
   from `useCheckTypes(org)`. Extend `validateSearch`/`isFiltering`/writers.
5. **i18n**: add keys to `en/fr/de/es` for status labels, "All statuses" /
   "All types", N-selected trigger format; replace hard-coded English strings.
6. **Backend test fixtures**: add a test-mode fixture that seeds/drives a
   check into `validating` for E2E, if the existing fixtures don't cover it.
7. **E2E**: toggle statuses (URL + rows + negative control), cold deep-link
   with two statuses, type filter alone + combined with status, validating
   check appears under `?status=validating`.
8. QA gate: `make build-backend lint-back test`, `make build-dash0`,
   `bun run lint`, `bun run test:unit`, then E2E via side-car server.
