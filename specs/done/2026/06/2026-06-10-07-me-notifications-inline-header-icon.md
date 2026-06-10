# My notifications: use the inline header icon to match sibling pages

## Context
The "My notifications" route
(`web/dash0/src/routes/orgs/$org/me.notifications.tsx`, lines 48-54) renders its
header with the shared `PageHeader` component, which draws the icon (`BellRing`)
inside a **boxed rounded square** (`h-10 w-10 rounded-md bg-muted`) next to a
`text-2xl font-semibold` title.

Every sibling list page uses a different, **inline** header: a plain
`h-7 w-7 text-muted-foreground` icon sitting directly next to a `text-3xl
font-bold tracking-tight` title, with no box — e.g. status-pages
(`Globe`), status-updates (`Megaphone`), incidents (`AlertTriangle`), discovery
(`Network`). As a result the My notifications header visibly stands out from the
rest of the app.

Per the agreed direction, the **inline icon style is canonical** for list pages
(documented in `2026-06-10-08-design-reference-header-patterns.md`), so this page
should adopt it.

## Goal
Replace the boxed `PageHeader` header on My notifications with the inline
icon + bold title pattern used by the other list pages, keeping the same icon,
title, and description text.

## Behaviour
Replace the current header:
```tsx
<PageHeader
  icon={BellRing}
  title="My pages"
  description="Incidents you were paged for, in reverse chronological order."
/>
```
with the inline pattern:
```tsx
<div>
  <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
    <BellRing className="h-7 w-7 text-muted-foreground" />
    My pages
  </h1>
  <p className="text-muted-foreground">
    Incidents you were paged for, in reverse chronological order.
  </p>
</div>
```
- Drop the `PageHeader` import; keep the `BellRing` import.
- Title and description strings are unchanged.
- The outer `div.space-y-6` and `data-testid="my-notifications-page"` stay.

## Out of scope
- No change to the notifications table, its links, or `useMyNotifications`.
- No change to the `PageHeader` component itself, nor to other pages still using
  it (e.g. integrations detail — a detail page, governed by the detail-header
  pattern, out of scope here).

## Testing
dash0 Playwright E2E (`web/dash0/e2e/`); coverage in `e2e/me-notifications.spec.ts`
(create/extend as needed).
- The page renders with `data-testid="my-notifications-page"` and the `h1`
  title/description.
- Manual: `make dev-test`, open `/me/notifications` next to `/status-pages` and
  `/incidents`, confirm the header icon + title now match (size, weight, no box);
  desktop + mobile, light + dark.

## Implementation Plan
1. Edit `me.notifications.tsx`: swap the `PageHeader` usage for the inline
   `h1` + `p` block; remove the `PageHeader` import.
2. Add/extend `e2e/me-notifications.spec.ts` if asserting the header.
3. Verify: `bun run lint` (dash0), `make test-dash`, manual side-by-side check.
