# Incident detail — action-button icons & layout

## Problem

On the incident detail page header
(`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`, header wrapper
≈ lines 602–724, action group ≈ lines 649–723) three things are off:

1. **The "Acknowledge" / "Unacknowledge" buttons have no icon**, unlike every
   other action in the row (Snooze → `BellOff`, Resolve → `CheckCircle`). They
   show only a label (plus a spinner while pending), which is visually
   inconsistent and gives nothing to fall back to when labels are hidden.
2. **The back / left-arrow button sits on the far left**, next to the title
   (inside `<div className="flex items-center gap-4">`), separated from the
   other controls. It should sit immediately to the **left of the right-hand
   action buttons**, grouping all controls together.
3. **Nothing is responsive.** The header has no breakpoint classes, so on a
   phone the labelled buttons (Acknowledge, Snooze, Resolve, …) overflow and
   wrap. On mobile every right-hand button should collapse to a single icon.

## Goal

A consistent, mobile-friendly action row: every button carries an icon, the
back button is the leftmost control of the right-hand group, and on small
screens the buttons render icon-only.

## Non-goals

- No change to button behaviour, handlers, ordering of the *actions* relative
  to each other, or to which buttons appear in which incident state
  (`isActive` / `acknowledgedAt` / `isSnoozed`).
- No backend or API change. Frontend-only.
- No change to the page title, badges, or the rest of the page below the header.

## Changes (frontend only)

File: `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`

### 1. Icons on every action button

Give each labelled button a leading icon (decision from the request: use a
"seen" icon for acknowledge, **not** a trash bin — the trash bin is reserved
for destructive/delete actions per `CLAUDE.md`):

| Button | Icon |
|---|---|
| Acknowledge | `Eye` (alt: `CheckCheck`) |
| Unacknowledge | `EyeOff` |
| Snooze | `BellOff` (unchanged) |
| Wake up (unsnooze) | `Bell` (currently has no icon) |
| Resolve | `CheckCircle` (unchanged) |
| Refresh | `RefreshCw` (unchanged, already icon-only) |

Add the missing imports (`Eye`, `EyeOff`, `Bell`) to the `lucide-react` import
block (≈ lines 5–18); `Bell`/`Eye`/`EyeOff` are not yet imported. Keep the
existing pending-spinner behaviour (`Loader2` swaps in for the icon while the
mutation `isPending`).

### 2. Move the back button into the right-hand group

- Remove the `ArrowLeft` ghost icon button from the left `flex items-center
  gap-4` container next to the title.
- Insert it as the **first (leftmost) child** of the right-hand action group
  (`<div className="flex items-center gap-2">`, ≈ line 649), before the Refresh
  button. Same `variant="ghost" size="icon"` and same `navigate(...)` onClick.
- The left side then holds only the status icon + title + badges.

### 3. Icon-only on mobile

Every button in the right-hand group shows icon-only on small screens and
icon + label from `sm:` up:

- Wrap each label in `<span className="hidden sm:inline">…</span>`.
- Change the leading-icon margin from `mr-2` to `sm:mr-2` so there's no dangling
  gap when the label is hidden.
- Add an `aria-label` (reuse the same `t("actions.*")` string as the label) to
  every button so the icon-only state stays accessible / screen-reader-friendly.
- Refresh and back are already `size="icon"` and need no change.

(Buttons keep their default `size`, so on mobile they render as a snug,
padded icon button. If a cleaner square is wanted, this is the place to switch
to `size="icon"`, but default padding is acceptable.)

## Conventions

- Start from the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) and reuse the shipped
  `Button` primitive. If the responsive icon-+-label button becomes a pattern
  worth cataloguing, add it to the reference page.
- Page must remain fully usable on mobile (this change is the point).
- Trash bin / destructive red stays reserved for delete actions — Acknowledge
  must **not** use `Trash2`.

## Acceptance criteria

1. Acknowledge shows an `Eye` icon; Unacknowledge shows `EyeOff`; Wake-up shows
   `Bell`. None of them uses a trash bin.
2. The back / left-arrow button renders as the leftmost button of the
   right-hand action group (to the left of Refresh), not next to the title.
3. On a narrow viewport every right-hand button shows only its icon (no text);
   from `sm:` up they show icon + label. No horizontal overflow / wrapping on a
   phone-width screen.
4. Each icon-only button is reachable by screen readers (has an `aria-label`).
5. Pending state still shows the spinner in place of the icon; clicking still
   acknowledges / unacknowledges / snoozes / wakes / resolves / refreshes /
   navigates back exactly as before.

## Implementation plan

1. Add `Eye`, `EyeOff`, `Bell` to the `lucide-react` import block.
2. Add the leading icons + `aria-label`s to Acknowledge, Unacknowledge, Wake-up
   (and confirm Snooze/Resolve already have them).
3. Move the `ArrowLeft` button into the right-hand group as its first child;
   drop it from the title container.
4. Wrap each label in `hidden sm:inline` and switch icon margins to `sm:mr-2`.
5. Verify on a phone-width viewport (no overflow, icon-only) and desktop
   (icon + label); add/adjust a Playwright check in `web/dash0/e2e/` if an
   incident-detail spec exists.
