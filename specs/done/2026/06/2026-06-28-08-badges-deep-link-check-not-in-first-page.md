# Badges page: deep-link / dropdown fails for checks beyond the first page

## Context

The badges route
([`web/dash0/src/routes/orgs/$org/badges.tsx`](../../web/dash0/src/routes/orgs/$org/badges.tsx))
lets an operator generate status/availability/uptime badges for a check. The
check is chosen either:

- via the `check` **search param** — e.g.
  `…/dash0/orgs/default/badges?check=http-cloudflare-dns` — which is exactly how
  the **check detail page** links here (the Badges button builds
  `search={{ check: check.slug ?? checkUid }}`,
  [`checks.$checkUid.index.tsx:608`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)); or
- via the `Select` **dropdown** on the page.

Both paths are backed by a single list query: `useChecks(org)`
([`badges.tsx:334`](../../web/dash0/src/routes/orgs/$org/badges.tsx)), called
**with no `limit`**. The page then resolves the selected check purely against
that in-memory list:

```ts
const selectedCheck = search.check
  ? checks.find((c) => c.uid === search.check) ??
    checks.find((c) => c.slug === search.check)
  : undefined;
```

and the dropdown maps over the same `checks` array.

## The bug (reproduced)

`GET /api/v1/orgs/{org}/checks` **defaults to `limit=20`, max `100`**
([`server/internal/handlers/checks/handler.go:144`](../../server/internal/handlers/checks/handler.go) —
"Parse limit (default 20, max 100)"). It is a paginated, cursor-based endpoint.
`useChecks(org)` passes no limit, so the badges page only ever sees the **first
20 checks**.

When an org has more than 20 checks, any check past the first page is invisible
to this page:

- `selectedCheck` resolves to `undefined`, so the page renders the empty
  **"Select a check to preview"** prompt even though the URL explicitly names a
  valid check. The deep-link silently does nothing.
- The dropdown lists only 20 of N checks, so the operator can't manually pick
  the missing check either.

Reproduced on the local dev `default` org (`make dev-test`), which has **42
checks** (`pagination.total: 42`, `limit: 20`):

```
GET /api/v1/orgs/default/checks                 → 20 items (first page)
GET /api/v1/orgs/default/checks?limit=100       → 42 items
  └─ "http-cloudflare-dns" (Cloudflare DNS, uid ddfbb2fa-…) is at index 40
     → NOT in the first 20 → selectedCheck = undefined → empty prompt
```

The **badge image API is not at fault** — it resolves the slug fine and returns
a valid SVG:

```
GET /api/v1/orgs/default/checks/http-cloudflare-dns/badges/status → HTTP 200, image/svg+xml
```

So the badge a user copies from this page would render correctly; the page just
refuses to *show* it. The fault is entirely the dashboard page's truncated
client-side list.

This also means the check detail → Badges button is broken for any check that
sorts beyond position 20 in its org — the very deep-link the app generates lands
on an empty page.

## Root cause

The page treats a **paginated** list endpoint as if it returned every check,
and resolves the selected check only from that partial page. There are two
independent gaps:

1. **Deep-link resolution** must not depend on the check being present in the
   list page.
2. **Dropdown completeness** — the operator should be able to pick any check,
   and the currently-selected check must always be a valid `<SelectItem>` (Radix
   `Select` shows a blank trigger when `value` has no matching item).

## Goal

Opening `…/badges?check=<uid-or-slug>` for **any** valid check in the org always
resolves that check and renders the badge configurator + preview, regardless of
how many checks the org has or where the check sorts. The dropdown lets the
operator reach checks beyond the first page.

## Behaviour

1. **Resolve the selected check independently of the list.** Use the
   single-check endpoint, which resolves **uid or slug** (verified:
   `GET /api/v1/orgs/{org}/checks/http-cloudflare-dns` → 200 with the full
   check; handler "GetCheck … by UID or slug",
   [`checks/handler.go:247`](../../server/internal/handlers/checks/handler.go)).
   `useCheck(org, uid)` ([`hooks.ts:262`](../../web/dash0/src/api/hooks.ts))
   already wraps it and is `enabled: !!org && !!uid`, so it no-ops when no
   `check` param is set.

   Resolution order for `selectedCheck`: list match by uid → list match by slug
   → the directly-fetched check. This guarantees the preview renders for any
   valid deep-link even when the check is outside the loaded list page.

2. **Make the dropdown complete (within reason).** Raise the list query to the
   endpoint max, `useChecks(org, { limit: 100 })`, so the dropdown covers up to
   100 checks instead of 20.

3. **Guarantee the selected check is a valid option.** Build the dropdown's
   options from the list plus the directly-resolved `selectedCheck` when it
   isn't already in the list (dedupe by `uid`), so the `Select` trigger shows
   the correct check name even when the selection came from a deep-link to a
   check outside the first 100.

4. **Don't flash the empty prompt while resolving.** When `search.check` is set
   and the direct `useCheck` fetch is still in flight, show the loading state
   (or nothing), not the "Select a check to preview" prompt. Only show the empty
   prompt when there is genuinely no `check` param.

5. **Handle a stale / unknown check param.** If `search.check` is set but
   resolution fails (deleted check → `useCheck` 404s and it isn't in the list),
   show a short "check not found" notice instead of the generic empty prompt, so
   a broken bookmark is legible rather than silently blank. Keep it minimal —
   reuse an existing alert/empty-state pattern from the design reference.

Everything else on the page (components/period/style controls, preview, embed
snippets, the back-to-check link, the `check` search-param storage format —
slug when available, else uid) stays exactly as-is.

## Out of scope

- **Orgs with >100 checks picking a non-deep-linked check from the dropdown.**
  Raising the limit to the endpoint max (100) plus the merge-selected-check
  guarantees deep-links always work and the dropdown covers the realistic case.
  Manually browsing to check #101+ that was never deep-linked remains
  unreachable from the plain dropdown. The proper long-term fix is a
  **searchable/async `Combobox`** that queries the list endpoint with `q=`
  (server-side search already supported). Note this limitation in the spec/PR;
  do not build the combobox here.
- No change to the badge image API, the check list endpoint, its default/max
  limit, or the `check` param format.
- No change to the check detail page's Badges button.

## Implementation

In [`web/dash0/src/routes/orgs/$org/badges.tsx`](../../web/dash0/src/routes/orgs/$org/badges.tsx):

1. Import `useCheck` alongside the existing `useChecks`
   (`import { useChecks, useCheck, type Check } from "@/api/hooks";`).

2. In `BadgesPage`:
   - `const { data: checks = [], isLoading, error } = useChecks(org, { limit: 100 });`
   - `const { data: directCheck, isLoading: directLoading } = useCheck(org, search.check ?? "");`
     (disabled automatically when `search.check` is falsy via `enabled: !!uid`).
   - Resolve:
     ```ts
     const selectedCheck = search.check
       ? checks.find((c) => c.uid === search.check) ??
         checks.find((c) => c.slug === search.check) ??
         directCheck
       : undefined;
     ```
   - Build options for the dropdown so the selection is always present:
     ```ts
     const checkOptions =
       selectedCheck && !checks.some((c) => c.uid === selectedCheck.uid)
         ? [selectedCheck, ...checks]
         : checks;
     ```
     Render the `Select` items from `checkOptions` instead of `checks`.

3. Gate the right-hand pane:
   - render the configurator/preview when `selectedCheck` is defined;
   - when `search.check` is set but unresolved **and** still loading
     (`directLoading`), render a skeleton/loading state, not the empty prompt;
   - when `search.check` is set but resolution finished with nothing
     (deleted/unknown check), render a "check not found" notice;
   - only when there is no `check` param, render the existing
     "Select a check to preview" prompt.

4. Keep `handleCheckChange`, `updateSearch`, the back-to-check link, and all
   controls unchanged. (Selecting from the dropdown still writes
   `?check=slug`; `useCheck` then resolves it — React Query dedupes against the
   list cache, so no extra round-trip for in-page checks.)

## Testing

dash0 Playwright E2E — extend the existing
[`web/dash0/e2e/badges.spec.ts`](../../web/dash0/e2e/badges.spec.ts):

- **Deep-link beyond the first page (regression test for this bug).** Ensure the
  test org has >20 checks (seed/create enough, or pick a fixture that does),
  then navigate to `…/badges?check=<slug-of-a-check-that-sorts-past-index-20>`
  and assert the badge preview (`[data-testid="badge-preview-img"]`) and embed
  URL render — i.e. the "select a check" prompt is **not** shown.
- **Deep-link by uid** for the same out-of-page check resolves identically.
- **Dropdown** shows the deep-linked check's name in the trigger even when it's
  outside the first page (merge path).
- **Unknown check param** (`?check=does-not-exist`) shows the not-found notice,
  not a blank pane and not a redirect.
- **No `check` param** still shows the "Select a check to preview" prompt.

Manual: `make dev-test`, open
`http://localhost:4000/dash0/orgs/default/badges?check=http-cloudflare-dns`
(check #40 of 42 on the dev org) and confirm the Cloudflare DNS badge preview
renders; confirm desktop + mobile, light + dark.

## QA

`bun run lint` and `bun run build` (tsc) green in `web/dash0`; zero NEW dash0
eslint errors vs the committed baseline; `make test-dash` passes.

## Files referenced

- [`web/dash0/src/routes/orgs/$org/badges.tsx`](../../web/dash0/src/routes/orgs/$org/badges.tsx) — the page to fix (`useChecks(org)` at L334, `selectedCheck` at L356-359, `Select` items at L451-455)
- [`web/dash0/src/api/hooks.ts`](../../web/dash0/src/api/hooks.ts) — `useChecks` (L220, accepts `limit`), `useCheck` (L262, resolves uid-or-slug, `enabled: !!uid`)
- [`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx) — source of the deep-link (Badges button, L608)
- [`server/internal/handlers/checks/handler.go`](../../server/internal/handlers/checks/handler.go) — list default/max limit (L144-157), GetCheck by uid-or-slug (L247-268)
- [`web/dash0/e2e/badges.spec.ts`](../../web/dash0/e2e/badges.spec.ts) — extend with the regression coverage

## Implementation Plan

Frontend-only change, confined to `web/dash0`. No backend, no API, no param-format
changes.

### Step 1 — Resolve the selected check independently of the list page
File: `web/dash0/src/routes/orgs/$org/badges.tsx`.
- Import `useCheck` alongside `useChecks`:
  `import { useChecks, useCheck, type Check } from "@/api/hooks";`.
- Raise the list query to the endpoint max so the dropdown covers up to 100 checks:
  `const { data: checks = [], isLoading, error } = useChecks(org, { limit: 100 });`.
- Add the single-check fetch (auto-disabled when `search.check` is falsy via the
  hook's `enabled: !!org && !!uid`):
  `const { data: directCheck, isLoading: directLoading } = useCheck(org, search.check ?? "");`.
- Resolution order list-uid → list-slug → directly-fetched check:
  ```ts
  const selectedCheck = search.check
    ? checks.find((c) => c.uid === search.check) ??
      checks.find((c) => c.slug === search.check) ??
      directCheck
    : undefined;
  ```

### Step 2 — Guarantee the selected check is always a valid `<SelectItem>`
- Build `checkOptions` by prepending the resolved `selectedCheck` to the list when it
  isn't already present (dedupe by `uid`):
  ```ts
  const checkOptions =
    selectedCheck && !checks.some((c) => c.uid === selectedCheck.uid)
      ? [selectedCheck, ...checks]
      : checks;
  ```
- Render the `Select` items from `checkOptions` instead of `checks` so the Radix
  trigger shows the correct name for a deep-link outside the first page.

### Step 3 — Gate the right-hand pane (no empty-prompt flash; not-found notice)
Replace the binary `selectedCheck ? preview : prompt` with four states:
1. `selectedCheck` defined → render the configurator/preview (unchanged
   `BadgePreview`).
2. `search.check` set, unresolved, and `directLoading` → render a loading skeleton
   (reuse `Skeleton`), not the prompt.
3. `search.check` set, resolution finished with nothing (deleted/unknown) → render a
   "check not found" notice. Reuse the design-reference `Alert` primitive
   (`variant="warning"` + `AlertCircle` icon), `data-testid="badge-check-not-found"`.
4. No `check` param → existing "Select a check to preview" prompt (unchanged).
- Add i18n key `checkNotFound` to all four `badges.json` locale files (en/fr/es/de).

### Step 4 — Leave everything else untouched
`handleCheckChange`, `updateSearch`, the back-to-check link, all controls, the
`check` param format (slug-when-available-else-uid) stay exactly as-is.

### Step 5 — E2E regression coverage
Extend `web/dash0/e2e/badges.spec.ts`:
- Seed >20 checks in the `test` org, then deep-link by slug to a check that sorts
  past index 20; assert preview + embed URL render and the prompt is absent
  (regression test).
- Same out-of-page check by uid resolves identically.
- Dropdown trigger shows the deep-linked out-of-page check's name (merge path).
- `?check=does-not-exist` shows the not-found notice (no blank pane, no redirect).
- No `check` param still shows the "Select a check to preview" prompt (already
  covered by the existing first test).
Because the local `:4000` devloop is usually not in `SP_RUNMODE=test`, these may be
authored-but-not-run locally.

### Limitation (carry into PR)
Orgs with >100 checks: a check that sorts at position 101+ and was never deep-linked
stays unreachable from the plain dropdown. The long-term fix is a searchable/async
`Combobox` querying the list endpoint with `q=`; out of scope here.

### QA
`bun run lint` + `bun run build` (tsc) green in `web/dash0`, zero NEW eslint errors
in touched files, `make build-dash0` green. `make test-dash` for the badges spec
(run if the local env permits `test`-org login; otherwise authored-but-not-run).
</content>
