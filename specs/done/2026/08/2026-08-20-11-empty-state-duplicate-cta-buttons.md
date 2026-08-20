---
model: sonnet
effort: low
---

# Empty-state cards still carry a duplicate create button on five list pages

## Problem

The empty-collection design was supposed to converge after
`2026-08-20-08-list-page-toolbar-and-empty-state-consistency`, but five pages
still render a CTA button *inside* the empty-state card that duplicates the
button already present in the page header (top right). Maintenance Windows is
the reference: its empty card is icon + title + hint, **no button**
([maintenance-windows.index.tsx:365-375](../../web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx)).

Pages still showing the duplicate in-card button:

| Page | In-card button to remove |
|---|---|
| Status Updates | "New Status Update" — [status-updates.index.tsx:438-443](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx) |
| Escalation policies | "New policy" — [escalation-policies.index.tsx:192-197](../../web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx) |
| On-call schedules | "Create schedule" — [on-call.index.tsx:133-138](../../web/dash0/src/routes/orgs/$org/on-call.index.tsx) |
| My pages | "Notification settings" (outline) — [me.notifications.tsx:90-95](../../web/dash0/src/routes/orgs/$org/me.notifications.tsx) |
| SLOs | "New objective" — [slos.index.tsx:182-188](../../web/dash0/src/routes/orgs/$org/slos.index.tsx) |

The root cause is that the **design reference prescribes the duplicate**: the
"Empty state" section says a truly empty list "gets a primary CTA button
linking to the page's create route"
([design-reference.tsx:3070-3100](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)),
and both its live preview and the copyable `importLine` snippet include a
"Create your first check" button. As long as the catalog shows the button,
new pages will keep reintroducing it.

## Proposal

1. **Update the design reference first** (it is the single source of truth):
   - Rewrite the "Empty state: no rows at all" prose at
     `design-reference.tsx:3076-3079` to state the new rule: the empty card is
     icon + title + hint only — **no CTA button**. The page's create action
     lives once, in the page header (top right); the toolbar above the card
     already stays rendered.
   - Remove the `<Button>` ("Create your first check") from the live preview
     (`design-reference.tsx:3093-3096`) and from the `importLine` snippet
     (`design-reference.tsx:3099`).
   - Leave the "Empty state: no search matches" section as is (it already has
     no CTA) — just make sure the two sections read consistently.

2. **Remove the in-card buttons** from the five pages listed above, matching
   the Maintenance Windows structure exactly (icon circle, title, hint,
   nothing else). Clean up imports that become unused in each file (`Plus`,
   `Button`, `Link`, `Settings`, `navigate`, …).

3. **Verify nothing targets the removed buttons.** The E2E suite drives
   creation through the header buttons (e.g. `status-updates-new` test-id in
   `e2e/status-updates.spec.ts:31`), so no test changes are expected — but
   grep `web/dash0/e2e/` for the removed labels/test-ids to be sure, and run
   the affected specs.

4. **Sweep for stragglers**: grep the remaining `*.index.tsx` list routes for
   `<Button>` inside empty-state blocks so no other page keeps the old
   pattern (the checks list is already clean).

Lint scope: dash0 `eslint .` has ~25 pre-existing errors on base; the bar is
no *new* errors, not fixing the debt.
