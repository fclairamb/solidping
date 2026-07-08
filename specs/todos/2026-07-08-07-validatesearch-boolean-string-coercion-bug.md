# `validateSearch` boolean filters silently no-op because they compare against the wrong type

## Problem

TanStack Router's default search parser coerces `"true"`/`"false"` query-string
values into native booleans **before** a route's `validateSearch` runs. Several
routes' `validateSearch` implementations don't account for this and compare the
already-coerced value against the string `"true"`, which is never true — so a
URL like `?foo=true` silently resolves the field to `undefined`/`false` instead
of enabling the filter.

This was first found and fixed in
[`web/dash0/src/routes/orgs/$org/login.tsx`](web/dash0/src/routes/orgs/$org/login.tsx)
while working spec `2026-07-08-01` (per-org session duration / token refresh,
since merged into `batch/2026-07-08`): its `validateSearch` did
`session_expired: search.session_expired === "true"`, which never matched. Fix
applied there:

```ts
session_expired: search.session_expired === true || search.session_expired === "true",
```

This mirrors the pattern already used correctly for the `allOrgs` param in
[`jobs.$jobUid.tsx`](web/dash0/src/routes/orgs/$org/jobs.$jobUid.tsx),
[`jobs.index.tsx`](web/dash0/src/routes/orgs/$org/jobs.index.tsx), and
[`jobs.check.$checkJobUid.tsx`](web/dash0/src/routes/orgs/$org/jobs.check.$checkJobUid.tsx).

### Two more known occurrences (unfixed)

Found via `grep -rn '=== "true"' web/dash0/src/routes/`:

- [`incidents.index.tsx:43`](web/dash0/src/routes/orgs/$org/incidents.index.tsx:43)
  — `showSuppressed: search.showSuppressed === "true" ? true : undefined,`
  A URL like `?showSuppressed=true` silently fails to show suppressed
  incidents.
- [`checks.$checkUid.index.tsx:84`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:84)
  — `graphFull: search.graphFull === "true" ? true : undefined,`
  A URL like `?graphFull=true` silently fails to expand the response-time
  chart to full width/range.

There may be other, not-yet-found instances of the same anti-pattern
elsewhere in the routes tree (any bare `=== "true"` or `=== "false"` string
comparison without the boolean-typed companion check).

## Proposal

1. Fix `incidents.index.tsx:43` and `checks.$checkUid.index.tsx:84` using the
   same `=== true || === "true"` (and `=== false || === "false"` where
   relevant) pattern as the `login.tsx` and `jobs.*` fixes.
2. Grep the whole `web/dash0/src/routes` tree for the anti-pattern (`=== "true"`
   or `=== "false"` without a matching `=== true`/`=== false` check) and fix any
   further occurrences the same way.
3. Check whether an existing Playwright E2E test
   (`web/dash0/e2e/`) exercises `showSuppressed` or `graphFull` via a **direct
   URL** (not just in-app navigation/clicks) — that's the only way this class
   of bug reproduces, since in-app navigation constructs the search object
   directly rather than round-tripping through the URL parser. If no such test
   exists, add one per fixed param (navigate directly to
   `...?showSuppressed=true` / `...?graphFull=true` and assert the resulting
   UI state), so a regression here is caught by CI rather than by manual
   testing.
4. Run `make build-dash0` and `cd web/dash0 && bun run lint` to confirm no
   regressions before committing.

## Open questions

- None identified yet — scope should be confirmed complete once the grep sweep
  in step 2 comes back clean.

## Implementation Plan

1. Fix `incidents.index.tsx:43` (`showSuppressed`) and
   `checks.$checkUid.index.tsx:84` (`graphFull`) with the
   `=== true || === "true"` pattern from `login.tsx`'s `session_expired` fix.
2. Grep sweep of `web/dash0/src/routes` for `=== "true"`/`=== "false"`
   confirms these are the only two remaining unfixed occurrences —
   `login.tsx` and the three `jobs.*.tsx` files already carry the boolean
   companion check (`=== true ||`/`=== false ||`) and are left untouched.
   Unrelated `"true"`/`"false"` string literals in `checks.index.tsx`
   (a `<Select>` value, not `validateSearch`) and `design-reference.tsx`
   (HTML attributes) are out of scope.
3. Add Playwright E2E coverage: check `web/dash0/e2e/` for existing direct-URL
   navigation to `?showSuppressed=true` / `?graphFull=true`; add tests if
   missing, per fixed param.
4. Run `make build-dash0` and `cd web/dash0 && bun run lint`; fix any new
   findings before committing.
