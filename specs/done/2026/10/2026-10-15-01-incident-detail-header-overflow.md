---
model: sonnet
effort: medium
---

# Incident detail header overflows at 1280x800: Resolve button is cut off

## Problem
On `/d/orgs/<org>/incidents/<uid>` at 1280x800 with the sidebar expanded, the header of an active, escalated, flapping incident ("#13 checkout is down", badges "Active", "Escalated", "flapping ×1") does not fit. The title wraps onto two lines and the action row (back arrow, Refresh, Acknowledge, Snooze..., Resolve) runs past the right edge. Resolve shows as "Re…" and needs horizontal scroll.

Cause: the header in `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:892` is a single non-wrapping row. `flex items-center justify-between` holds a title group (`:893`, with `text-3xl` h1 and badges) and an action group (`:949`, `flex items-center gap-2`). Neither side has `flex-wrap` or `min-w-0`, and the action group has no `shrink-0`/wrap rule. The buttons show their labels from `sm:` up (Refresh uses `hidden sm:inline`), which is far too wide at 1280 minus the sidebar.

## Proposal
1. In `incidents.$incidentUid.tsx:892-949`, make the header wrap instead of overflow:
   - outer row becomes `flex flex-wrap items-start justify-between gap-3`;
   - title group gets `min-w-0 flex-1`, and the badge row gets `flex-wrap`;
   - action group gets `flex flex-wrap items-center gap-2` so it drops below the title when it does not fit (full width on narrow screens).
2. Keep every button visible (no overflow menu) unless the wrapped layout still fails at 375px. If it does, move Refresh and Snooze into a `DropdownMenu` using the pattern in `src/routes/orgs/$org/design-reference.tsx` (see `DropdownMenu` usage at ~`:493`/`:529`). Give any new element a `data-testid`.
3. Add `data-testid` to the Resolve, Acknowledge, Snooze and Refresh buttons if missing (`:975-1042`) so the test does not rely on text.
4. Do not change any string. If one does change, update `src/locales/{en,fr,de,es}/incidents.json`.
5. Check the design-reference page needs no update (only if a new primitive or pattern is introduced).

## Tests
- `web/dash0/e2e/incidents.spec.ts` (extend the test at `:95`, or add one next to it): create an active, escalated, flapping incident (use the fixtures in `e2e/fixtures.ts`/`section-helpers.ts`). The existing test skips when no incident exists, so seed one rather than skip. At 1280x800 with the sidebar expanded, assert the Resolve button's `boundingBox()` has `x + width <= viewport width`, assert `document.documentElement.scrollWidth <= clientWidth + 1`, and assert Resolve is clickable (`toBeVisible` plus `click` trial).
- Same assertions at 375x812 (negative/regression case: no horizontal overflow, Resolve reachable).
- Confirm the test fails before the fix (reproduces the bug), then passes.
- Run `bun run lint:e2e`, `bun run typecheck:e2e` and `incidents.spec.ts` from `web/dash0` (side-car server, not the `:4000` devloop; see `wiki/testing/local-e2e.md`).

## To verify
- Whether an existing fixture can produce an escalated + flapping incident. If not, how to seed one (API or test-mode data) for the e2e.
- Whether the sidebar is expanded by default in the e2e context at 1280x800 (it should be; assert it).
