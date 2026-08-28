---
model: sonnet
effort: medium
---

# CheckMultiPicker chips show raw UUIDs for checks outside the current search page

## Problem

On forms that use `CheckMultiPicker` — the report-schedule form
(`web/dash0/src/components/slos/report-schedule-form.tsx`) and the
maintenance-window form
(`web/dash0/src/components/shared/maintenance-window-form.tsx`) — the selected
checks are rendered as removable Badge chips. For some checks the chip shows
the raw UUID (e.g. `57c34008-6f53-4794-b2fa-2…`) instead of the check's name.

The bug reproduces on any org with more than ~25 checks: open an existing
report schedule (edit route `organization.report-schedules.$uid.tsx`) whose
selected checks are not among the first page of search results, and the chips
for those checks display their UUIDs. Newly picked checks render fine (they
were just present in the search results), which is why the breakage looks
intermittent — it depends on whether each selected check happens to be in the
currently fetched page.

Root cause, in
`web/dash0/src/components/shared/check-multi-picker.tsx`:

- The picker fetches checks with `useChecks(org, { q, limit: SEARCH_LIMIT })`
  where `SEARCH_LIMIT = 25` (`check-multi-picker.tsx:13,52-55`).
- Chip labels are resolved exclusively from those current search results via
  the `labelFor` map (`check-multi-picker.tsx:78-89`), with an explicit
  fallback to the raw uid: `labelFor.get(uid) ?? uid`
  (`check-multi-picker.tsx:182`). The comment at lines 75-77 acknowledges the
  fallback, but on an edit form loading a persisted selection the miss is the
  common case, not an edge case.
- Worse, the map is rebuilt from the *current* query's results, so a chip that
  briefly had a name can regress to a UUID as the user types a search that
  excludes it.

The single-value `CheckPicker`
(`web/dash0/src/components/shared/check-picker.tsx:64-100`) already solved
this exact problem: when the selected uid can't be resolved from search
results, it fetches the individual check with `useCheck(org, uid)` (cache key
`["check", org, uid]`, shared with other consumers) and only falls back to the
raw uid when the fetch errors (check genuinely deleted/inaccessible). The
multi-picker never received the same treatment.

`kind="groups"` is not affected — groups are fetched whole and the label map
always includes all of them (`check-multi-picker.tsx:81-82`).

## Proposal

Bring `CheckMultiPicker` up to parity with `CheckPicker`'s label resolution:

1. **Resolve unknown selected uids individually.** For each uid in `value`
   that is not resolvable from the current search results, fetch the check
   with TanStack Query's `useQueries` (one query per unresolved uid, same
   `["check", org, uid]` key and fetch function as `useCheck` so the cache is
   shared and edits elsewhere keep it warm). Only run these queries for
   `kind === "checks"`.

2. **Make resolved labels sticky.** Keep an accumulating label map (e.g. a
   ref-backed `Map` merged with each render's search results and per-uid
   fetches) so a chip that has ever displayed a name never regresses to a
   UUID while the user types a narrowing search.

3. **Fallbacks.** While a uid is still resolving, show a placeholder ("…", as
   `CheckPicker` does) rather than the UUID; if the fetch fails (deleted
   check), fall back to the uid so the chip stays visible and removable —
   matching `check-picker.tsx:93-96`.

4. **Tests.** Add/extend Playwright coverage in `web/dash0/e2e/`: seed more
   checks than one search page (>25), create a report schedule (or
   maintenance window) selecting a check from beyond the first page, reload
   the edit route, and assert the chip shows the check's *name* — plus a
   negative control asserting no chip text matches a UUID pattern. Verify the
   maintenance-window form benefits too, since it uses the same component.

Out of scope / notes:

- An alternative would be a server-side `uid` (comma-separated, singular per
  REST conventions) filter on `GET /orgs/:org/checks` to batch-resolve; not
  required here since the per-uid `GET /orgs/:org/checks/:uid` pattern already
  exists, shares cache, and selections are small. If the implementer prefers
  the batch filter, it must land with handler tests on both dialects.
- Do not use real customer/org names or hostnames in fixtures — use `acme`
  per repo policy.
