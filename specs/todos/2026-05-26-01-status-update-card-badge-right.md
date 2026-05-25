# Move status-update badge to the right of the card header

## Problem

In `web/status0/src/components/shared/status-update-card.tsx`, the kind badge
("Identified", "Resolved", …) sits **before** the title on the left, which
visually interrupts the title text.

Current layout:
```
[Identified] Test                              6 days ago
Hello
```

Desired layout:
```
Test                              [Identified] 6 days ago
```

## Change

In `StatusUpdateCard`, restructure the header row so that:

- **Left**: `<h3>` title only (no badge)
- **Right**: kind `<Badge>` immediately followed by the `<time>` timestamp,
  grouped together with a small gap (`gap-2`), both `shrink-0`

The badge and timestamp should be wrapped in a single `<div className="flex items-center gap-2 shrink-0">` on the right side.

## Scope

Single file: `web/status0/src/components/shared/status-update-card.tsx`

No backend, no API, no other component changes needed.

## Acceptance criteria

- Badge appears to the right of the title, next to the date.
- Title starts at the left edge of the card without any badge before it.
- On narrow viewports the right group (badge + timestamp) wraps below the title
  rather than overflowing (keep `flex-wrap` on the outer row).
- All existing badge colours and variants are preserved.

## Implementation Plan

1. **Restructure the header row in `StatusUpdateCard`** (`web/status0/src/components/shared/status-update-card.tsx`):
   - Remove the `<Badge>` from the left `<div>` that currently wraps badge + title.
   - Keep the `<h3>` title as the only left-side element (direct child or in a minimal wrapper).
   - Create a new right-side `<div className="flex items-center gap-2 shrink-0">` that contains the `<Badge>` followed by the `<time>` element.
   - Ensure the outer row keeps `flex-wrap` so the right group wraps below on narrow viewports.

2. **Verify build** — run `make build-client` to confirm TypeScript compiles cleanly.

3. **Verify lint** — run `make lint` to confirm no lint errors are introduced.
