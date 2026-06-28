# Status Pages list: remove the redundant empty-state CTA and move Refresh beside "New Status Page"

## Context

The **Status Pages** list page
([`web/dash0/src/routes/orgs/$org/status-pages.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx),
route `/orgs/$org/status-pages`) has two layout quirks versus the rest of dash0
(see screenshot — title + "New Status Page" top-right, a search field with a
"Refresh" button to its right, and an empty state with a second "Create your
first status page" button):

1. **A duplicate primary action when the list is empty.** The header already
   carries a persistent **"New Status Page"** button (the `PageHeader` `actions`
   prop, [`status-pages.index.tsx:175-182`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx#L175)).
   The "no pages yet" empty state renders a **second**, equivalent
   `Create your first status page` button
   ([`status-pages.index.tsx:244-253`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx#L244)),
   so an empty org shows the same create action twice on one screen. This mirrors
   the already-specced status-updates cleanup
   ([`2026-06-28-01-remove-status-updates-empty-state-new-button.md`](2026-06-28-01-remove-status-updates-empty-state-new-button.md)).

2. **Refresh lives in the wrong cluster.** The **Refresh** button sits in the
   search-toolbar row, to the right of the search input
   ([`status-pages.index.tsx:196-204`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx#L196)),
   while **"New Status Page"** sits up in the header. Other multi-action list
   pages put **both** the Refresh and the primary "New X" button together in the
   `PageHeader` `actions`, Refresh on the left — e.g. Network Discovery
   ([`discovery.index.tsx:124-147`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx#L124),
   `↻ Refresh` then `+ Start new scan`). The user wants Status Pages to match:
   **Refresh immediately to the left of "New Status Page"**.

### Relevant current code

Header (`actions` holds only the New button):

```tsx
<PageHeader
  icon={Globe}
  title={t("statusPages:title")}
  description={t("statusPages:subtitle")}
  actions={
    <Link to="/orgs/$org/status-pages/new" params={{ org }}>
      <Button>
        <Plus className="mr-2 h-4 w-4" />
        {t("statusPages:newStatusPage")}
      </Button>
    </Link>
  }
  className="flex-wrap"
/>

<div className="flex items-center gap-4">
  <div className="relative flex-1 max-w-sm">
    <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
    <Input
      placeholder={t("statusPages:searchPlaceholder")}
      value={search}
      onChange={(e) => setSearch(e.target.value)}
      className="pl-9"
    />
  </div>
  <Button
    variant="outline"
    onClick={() => refetch()}
    disabled={isRefetching}
    aria-label={t("common:refresh")}
  >
    <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
    <span className="hidden sm:inline">{t("common:refresh")}</span>
  </Button>
</div>
```

Empty state (no pages at all):

```tsx
) : (
  <div className="text-center py-12 text-muted-foreground">
    <p className="mb-2">{t("statusPages:noStatusPages")}</p>
    <Link to="/orgs/$org/status-pages/new" params={{ org }}>
      <Button>
        <Plus className="mr-2 h-4 w-4" />
        {t("statusPages:createFirst")}
      </Button>
    </Link>
  </div>
)}
```

`PageHeader` already wraps whatever it receives in `actions` inside a
right-aligned `flex shrink-0 items-center gap-2` cluster
([`page-header.tsx:39-41`](../../web/dash0/src/components/shared/page-header.tsx#L39)),
so passing a fragment of `[Refresh, New]` renders them side by side with Refresh
on the left — no extra wrapper needed.

## Decision

Two scoped edits, both in `status-pages.index.tsx` (plus removing one now-dead
i18n key from the four `statusPages.json` locale files):

1. **Delete the empty-state `Create your first status page` button.** The empty
   state keeps only the "No status pages configured yet" message; the
   always-present header button is the single way to create a page. Drop the
   `<p>`'s now-dangling `mb-2` (nothing follows it anymore).
2. **Move the Refresh button into the `PageHeader` `actions`, before
   "New Status Page".** The search-toolbar row is left with just the search
   input. This reproduces the canonical discovery/checks header
   (`↻ Refresh` then `+ New …`).

## Goals

- The empty org shows **one** create action — the header "New Status Page"
  button — and the empty state reads cleanly: just
  "No status pages configured yet" with no button and no leftover bottom-margin
  gap.
- The header's top-right action cluster reads **`↻ Refresh`  ·  `+ New Status
  Page`**, in that order (Refresh to the left of New Status Page), separated by
  the standard `gap-2`.
- The search-toolbar row below the header contains **only** the search input.
- Refresh behaviour is unchanged: same `onClick={() => refetch()}`,
  `disabled={isRefetching}`, spinning icon while refetching, `aria-label`
  "Refresh", and its label still collapses to icon-only below the `sm`
  breakpoint (`hidden sm:inline`).
- The "New Status Page" button, its link target
  (`/orgs/$org/status-pages/new`), the table, the "no match" empty state, and
  all other behaviour are unchanged.

## Out of scope

- The **"no search match"** empty state
  ([`status-pages.index.tsx:239-243`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx#L239)) —
  it has no button (just the Search icon + "No status pages match your search")
  and is left exactly as-is.
- Mobile-collapsing the **"New Status Page"** label to icon-only — the button
  keeps its current always-visible label; only Refresh collapses (its existing
  behaviour). `className="flex-wrap"` on `PageHeader` already lets the two-button
  cluster wrap on narrow viewports. (A separate concern, analogous to
  [`2026-06-28-02`](2026-06-28-02-discovery-scan-list-rows-mobile-button-details-column.md)
  for discovery — not done here.)
- Adding an icon to the empty state, or any other copy/styling change.
- The create/edit routes (`status-pages.new.tsx`,
  `status-pages.$statusPageUid.edit.tsx`), the detail page, the row actions, the
  subscriber view (`web/status0`), and the legacy `web/dash` app.

## Implementation

All UI edits in
[`web/dash0/src/routes/orgs/$org/status-pages.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx).

### 1. Move Refresh into the header `actions` (lines ~175-182)

Replace the single-child `actions` with a fragment holding Refresh first, then
the existing New button:

```tsx
actions={
  <>
    <Button
      variant="outline"
      onClick={() => refetch()}
      disabled={isRefetching}
      aria-label={t("common:refresh")}
    >
      <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
      <span className="hidden sm:inline">{t("common:refresh")}</span>
    </Button>
    <Link to="/orgs/$org/status-pages/new" params={{ org }}>
      <Button>
        <Plus className="mr-2 h-4 w-4" />
        {t("statusPages:newStatusPage")}
      </Button>
    </Link>
  </>
}
```

Keep `className="flex-wrap"` on the `PageHeader`. The Refresh `<Button>` markup
is moved verbatim from the toolbar row — same props, same handler.

### 2. Reduce the toolbar row to the search input (lines ~186-205)

The row no longer needs to be a flex container of two items; drop the outer
`flex items-center gap-4` wrapper and the search div's `flex-1`:

```tsx
<div className="relative max-w-sm">
  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
  <Input
    placeholder={t("statusPages:searchPlaceholder")}
    value={search}
    onChange={(e) => setSearch(e.target.value)}
    className="pl-9"
  />
</div>
```

### 3. Remove the empty-state CTA (lines ~244-253)

```tsx
) : (
  <div className="text-center py-12 text-muted-foreground">
    <p>{t("statusPages:noStatusPages")}</p>
  </div>
)}
```

### 4. Remove the now-dead i18n key

`statusPages:createFirst` is used **only** by the deleted button. Remove the
`"createFirst"` line (line 8) from all four locale files —
[`en`](../../web/dash0/src/locales/en/statusPages.json#L8),
[`fr`](../../web/dash0/src/locales/fr/statusPages.json#L8),
[`de`](../../web/dash0/src/locales/de/statusPages.json#L8),
[`es`](../../web/dash0/src/locales/es/statusPages.json#L8).
Leave `noStatusPages` and `noMatch` (both still used). Do **not** touch the
unrelated `tokens.createFirst` key in `account.json`.

**Imports stay as-is** — none becomes unused: `RefreshCw` and the Refresh
`Button` move (still used); `Plus`, `Link`, and `Button` are all still used by
the header "New Status Page" button (and `Link`/`Button` by the table rows).

No state, handler, or data-fetching changes — `refetch`/`isRefetching` already
come from `useStatusPages` and are simply referenced from the header now.

## Design reference

No new primitive and no design-reference change. The header pattern (Refresh +
primary "New" button together in `PageHeader` `actions`, Refresh on the left) is
already shipped on `discovery.index.tsx`; this change only adopts it. The
canonical components remain those in
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx).

## Verification

With `make dev-test` running, open `/dash0/orgs/{org}/status-pages`:

1. **Header:** the top-right cluster shows `↻ Refresh` then `+ New Status Page`,
   in that order with even spacing; the search row below holds only the search
   input. Clicking Refresh refetches (icon spins, button disables briefly);
   clicking "New Status Page" still navigates to `/status-pages/new`.
2. **Empty org** (no status pages — e.g. a fresh org, or delete all pages): the
   empty state shows just "No status pages configured yet" with **no** button
   beneath it and no extra gap under the message. Exactly **one** create action
   exists on the page (the header button).
3. **Non-empty:** with at least one page the table renders unchanged; the header
   still reads `↻ Refresh` · `+ New Status Page`.
4. **Search with no match:** typing a non-matching query shows the unchanged
   "No status pages match your search" state (Search icon + message), never a
   create button.
5. **Mobile (~390px):** Refresh collapses to its icon only; the action cluster
   wraps under the title if needed; no horizontal overflow.

## Tests

- The existing Playwright suite
  ([`web/dash0/e2e/status-pages.spec.ts`](../../web/dash0/e2e/status-pages.spec.ts))
  creates a page via the **header** button
  (`getByRole("link", { name: "New Status Page" })`, line 15), never the
  empty-state CTA, so removing the CTA does not break it.
  [`status-page-subscribers.spec.ts`](../../web/dash0/e2e/status-page-subscribers.spec.ts)
  is likewise unaffected. Run `make test-dash` to confirm both stay green.
- **Optionally extend** `status-pages.spec.ts` with a small assertion that, on
  the list page, the "Refresh" button and the "New Status Page" button live in
  the same header action cluster with Refresh first in DOM order (mirrors the
  existing back-arrow-ordering check at lines 45-57), and that an empty list
  shows no "Create your first status page" button.
- `bun run lint` and `bun run build` (tsc) in `web/dash0` pass with no new errors
  (confirm no import was left unused — none should be).

## Files referenced

- [`web/dash0/src/routes/orgs/$org/status-pages.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx)
  — move Refresh into `PageHeader` `actions` (before New), slim the toolbar row
  to the search input, and delete the empty-state CTA + its `mb-2`.
- [`web/dash0/src/locales/en/statusPages.json`](../../web/dash0/src/locales/en/statusPages.json)
  (+ `fr`/`de`/`es`) — remove the now-unused `createFirst` key.
- [`web/dash0/src/components/shared/page-header.tsx`](../../web/dash0/src/components/shared/page-header.tsx#L39)
  — the right-aligned `gap-2` action cluster that renders `actions` (reference,
  do not change).
- [`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx#L124)
  — canonical `Refresh` + primary "New X" header pattern (reference).
- [`web/dash0/e2e/status-pages.spec.ts`](../../web/dash0/e2e/status-pages.spec.ts)
  — existing E2E, unaffected (creates via the header button).
