# useCheck: unify the query key so a live check update fetches the check once, not twice

## Problem

On the check detail page, every realtime check-update hint triggers **two**
HTTP requests for the same check:

- `GET /api/v1/orgs/{org}/checks/{uid}`
- `GET /api/v1/orgs/{org}/checks/{uid}?with=last_result,last_status_change`

They are two distinct React Query cache entries because `useCheck` puts the
`with` option inside the query key
(`web/dash0/src/api/hooks.ts:305`,
`["check", org, uid, { with: options?.with }]`), and two always-mounted
consumers use different `with` values:

1. The org layout's breadcrumb resolves the check name with no `with`
   (`web/dash0/src/routes/orgs/$org.tsx:157`) → key
   `["check", org, uid, { with: undefined }]`.
2. The check detail page fetches the full payload
   (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:417`) → key
   `["check", org, uid, { with: "last_result,last_status_change" }]`.

When a live hint arrives, the `checkDetailRoot("check")` matcher in
`web/dash0/src/contexts/LiveEventsContext.tsx:83` matches on
`key[0]`/`key[1]` (org)/`key[2]` (uid) only — it ignores the `with`
segment — so **both** queries are invalidated and both refetch. Steady-state
result writes (kind `results`) hit this on every check run, so the duplicate
fetch happens continuously while a detail page is open.

## Proposal

Drop the `with` option from `useCheck` entirely: **always** fetch
`?with=last_result,last_status_change` and key the query
`["check", org, uid]` (no options segment). One canonical cache entry per
check, shared by every consumer — a live invalidation then produces exactly
one HTTP request, and the duplicate cannot reappear as new call sites are
added.

Why this is safe:

- All four call sites work with the superset payload:
  - breadcrumb (`web/dash0/src/routes/orgs/$org.tsx:157`) — needs only
    `name`;
  - detail page (`checks.$checkUid.index.tsx:417` and the
    `refetchInterval` re-subscription at `:465`) — needs the full payload;
  - edit form (`checks.$checkUid.edit.tsx:39`,
    `refetchOnMount: "always"`) — needs config fields, extra embeds are
    ignored;
  - badge picker (`badges.tsx:339`) — needs `name`/`uid`/`slug`.
- Server-side cost of embedding `last_result,last_status_change` is two
  indexed lookups per fetch; payload growth is negligible.
- The endpoint resolves by uid **or slug** (`GetCheckByUidOrSlug`)
  regardless of `with`, so the breadcrumb/detail slug-deep-link behavior is
  unchanged. Note the two entries only merge once the route param is
  canonical; that is already today's situation and not made worse.
- Observers with different `refetchInterval`s can share one query —
  React Query drives refetching per observer, so the detail page's
  interval keeps working while the breadcrumb observer has none.

### Changes

1. `web/dash0/src/api/hooks.ts:290` — remove the `with` option from
   `useCheck`; hardcode `with=last_result,last_status_change` in the
   `queryFn`; key becomes `["check", org, uid]`. Keep `refetchInterval`
   and `refetchOnMount` options as-is.
2. Update call sites that pass `with` (`checks.$checkUid.index.tsx:417`,
   `:465`) to drop it. Other call sites need no change.
3. `web/dash0/src/api/hooks.ts:347` — the `useUpdateCheck` invalidation
   `["check", org, uid]` already prefix-matches today and becomes an exact
   match; no change needed, just verify.
4. `web/dash0/src/contexts/LiveEventsContext.test.ts:63` (and the other
   fixtures) seed keys with a trailing `{}` segment — update them to the
   new 3-segment shape so tests mirror reality (the matcher ignores the
   4th segment, so this is cosmetic but keeps fixtures honest).
5. Verify no dash0 test/E2E asserts the plain (no-`with`) URL for the
   check detail fetch.

### Alternatives considered (rejected)

- **Narrow the `results`-kind invalidation** so it only matches check
  queries whose key carries `with` including `last_result`
  (`DEFAULT_QUERY_ROOTS.check.results` in `LiveEventsContext.tsx:126`): a
  plain check document has no embedded result, so refetching it on a
  steady-state result write is pure waste. Surgical, but keeps two cache
  entries alive and still double-fetches on status transitions
  (kind `checks`).
- **Align the breadcrumb's `with` with the detail page's** (one-liner):
  makes the two share a key, but the edit page still holds a third key
  shape and the coupling silently breaks if the detail page's `with` ever
  changes.

Both lose to structurally having a single key.

## Acceptance

- With the check detail page open and a live check-update hint arriving,
  the network tab shows exactly **one** `GET .../checks/{uid}...` request
  per hint.
- Breadcrumb, detail page, edit form, and badge picker still render
  correctly (name, last result, config seeding, slug resolution).
- `LiveEventsContext.test.ts` and dash0 lint/build pass with no new
  errors.

## Implementation Plan

1. **`web/dash0/src/api/hooks.ts` — `useCheck`**: drop the `with` field from
   the `options` type; hardcode `?with=last_result,last_status_change` in the
   `queryFn`; change the query key from
   `["check", org, uid, { with: options?.with }]` to `["check", org, uid]`.
   Keep `refetchInterval` and `refetchOnMount` options untouched.
2. **Call sites in `checks.$checkUid.index.tsx`** (`:417`, `:465`): remove the
   `with: "last_result,last_status_change"` field from both `useCheck` calls
   (the `:465` call keeps `refetchInterval`). No other call site passes `with`
   (breadcrumb `$org.tsx:157`, edit `checks.$checkUid.edit.tsx:39`, badge
   picker `badges.tsx:339`), so they need no change.
3. **`useUpdateCheck` invalidation** (`hooks.ts:347`,
   `["check", org, uid]`): now an exact key match instead of a prefix match —
   verify only, no edit.
4. **`LiveEventsContext.test.ts`**: rewrite the seeded `check` keys (and the
   assertions referencing them) from the 4-segment `["check", ORG, uid, {}]`
   shape to the new 3-segment `["check", ORG, uid]` shape so fixtures mirror
   the real query key. The `checkDetailRoot` matcher ignores the trailing
   segment, so behavior is unchanged; this keeps fixtures honest.
5. **E2E**: extend `web/dash0/e2e/live-updates.spec.ts` (the "first result
   appears live on the detail page" test) to assert the single-cache-entry
   behavior — a live hint must never fetch the bare (no-`with`) breadcrumb URL
   (which was the duplicate request), i.e. `bareFetches === 0`, while the
   canonical `?with=` fetch still fires. Deterministic regression guard for
   the double-fetch.
6. **QA**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new errors in
   touched files), and run the `LiveEventsContext` vitest file.
