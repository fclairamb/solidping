# Remove the redundant "New update" button from the Status updates empty state

## Context

The **Status updates** list page
([`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx),
route `/orgs/$org/status-updates`) renders **two** "New update" buttons when the
list is empty:

1. **Header action** — the persistent primary button in the top-right, passed to
   `PageHeader`'s `actions` prop
   ([`status-updates.index.tsx:256-265`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx#L256)).
   It carries `data-testid="status-updates-new"` and is always visible regardless
   of list state.

2. **Empty-state CTA** — a second, identical `New update` button inside the
   "No status updates yet." empty state
   ([`status-updates.index.tsx:407-412`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx#L407)).
   It only renders when `filtered.length === 0`.

When the page is empty both render at once (header at top-right **and** centered in
the empty state — see screenshot), which is visually redundant: the same action
appears twice on the same screen. The user asked to **remove the second one** (the
empty-state button) and keep the header button as the single entry point for
creating an update.

### Current empty-state block (committed baseline)

```tsx
) : (
  <div className="text-center py-12 text-muted-foreground">
    <Megaphone className="h-8 w-8 mx-auto mb-2 opacity-50" />
    <p className="mb-2">No status updates yet.</p>
    <Button asChild>
      <Link to="/orgs/$org/status-updates/new" params={{ org }}>
        <Plus className="mr-2 h-4 w-4" />
        New update
      </Link>
    </Button>
  </div>
)}
```

## Decision

Delete the `<Button>`/`<Link>` "New update" CTA from the empty state. The empty
state keeps only the megaphone icon and the "No status updates yet." message; the
always-present header button remains the sole way to create an update. Drop the
now-dangling `mb-2` on the `<p>` so the trailing message isn't left with a bottom
margin that has nothing after it.

## Goals

- Only **one** "New update" button on the page — the `PageHeader` header action.
- The empty state reads cleanly: megaphone icon + "No status updates yet." with no
  button and no leftover bottom-margin gap.
- The header button (`data-testid="status-updates-new"`), its link target
  (`/orgs/$org/status-updates/new`), and all other page behaviour are unchanged.

## Out of scope

- Touching the header "New update" button or any other control on the page
  (filters, search, refresh, row actions).
- Any change to the empty state's copy/icon beyond removing the button.
- Restyling other empty states elsewhere in dash0, or the legacy `web/dash` app.
- The create/edit routes (`status-updates.new.tsx`, `status-updates.$updateUid.edit.tsx`)
  and the shared `status-update-form` — untouched.

## Implementation

In [`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx),
replace the empty-state branch (currently lines ~403-414) so it no longer renders
the button:

```tsx
) : (
  <div className="text-center py-12 text-muted-foreground">
    <Megaphone className="h-8 w-8 mx-auto mb-2 opacity-50" />
    <p>No status updates yet.</p>
  </div>
)}
```

**Imports stay as-is** — do **not** remove any import:

- `Button` is still used by the header action and the per-row ghost icon buttons.
- `Plus` is still used by the header "New update" button.
- `Link` is still used by the header button, the row title link, and the row edit
  button.

No other code changes are required (no state, no handlers, no i18n — the page has
none).

## Design reference

No new primitive and no design-reference change. This only removes a duplicate
instance of the existing primary `Button`; the canonical components remain those in
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx).

## Verification

With `make dev-test` running, open `/dash0/orgs/{org}/status-updates` for an org
with **no** status updates (e.g. apply a search/filter that matches nothing, or a
fresh org):

1. The empty state shows the megaphone icon and "No status updates yet." with **no**
   "New update" button beneath it.
2. Exactly **one** "New update" button is on the page — the top-right header action;
   clicking it still navigates to `/status-updates/new`.
3. Vertical spacing in the empty state looks balanced (no extra gap under the
   message from a leftover `mb-2`).
4. With at least one update present, the table renders as before and the header
   button is unaffected.

## Tests

- The existing Playwright suite
  ([`web/dash0/e2e/status-updates.spec.ts`](../../web/dash0/e2e/status-updates.spec.ts))
  targets the header button via `getByTestId("status-updates-new")` (the empty-state
  button had no test id), so removing the empty-state CTA does **not** break it.
  Run `make test-dash` to confirm the suite stays green.
- `bun run lint` and `bun run build` (tsc) in `web/dash0` pass with no new errors
  (verify no now-unused import was introduced — none should be).

## Files referenced

- [`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)
  — remove the empty-state "New update" button (lines ~407-412) and drop the `<p>`'s
  `mb-2`.
- [`web/dash0/e2e/status-updates.spec.ts`](../../web/dash0/e2e/status-updates.spec.ts)
  — existing E2E, unaffected (targets the header button by test id).

## Implementation Plan

1. **Edit the empty-state branch** in
   `web/dash0/src/routes/orgs/$org/status-updates.index.tsx` (lines ~403-414):
   - Remove the `<Button asChild><Link …>…New update…</Link></Button>` CTA.
   - Drop the `mb-2` class on the `<p>No status updates yet.</p>` so no trailing
     bottom margin remains.
   - Resulting block: `<div>` with `<Megaphone …/>` + `<p>No status updates yet.</p>`.
   - Leave all imports (`Button`, `Plus`, `Link`, `Megaphone`) untouched — each is
     still used elsewhere on the page (header action, row actions, row title link).
2. **Format & verify**: `make fmt`, then QA — `make build-dash0`, `make build-backend`,
   `make lint-back`, `make test`, and `cd web/dash0 && bun run lint` (gate: no NEW
   eslint errors vs. the known ~25-error react-hooks base debt).
3. **E2E**: no new test needed — `web/dash0/e2e/status-updates.spec.ts` already
   targets the header button via `getByTestId("status-updates-new")`; the removed
   empty-state CTA had no test id. Confirm the existing spec still references only the
   header button (no assertion on a second/empty-state button) so it stays green.
4. **Archive** the spec to `specs/done/2026/06/`.
