---
model: sonnet
effort: medium
---

# A check can only be created directly into a group while that group is empty

## Problem

On the checks list (`/orgs/$org/checks`), the only affordance for creating a
check **pre-assigned to a group** is the "Add a check to this group" link in the
group's *empty state* — [checks.index.tsx:886-900](web/dash0/src/routes/orgs/$org/checks.index.tsx:886):

```tsx
) : (
  <div className="p-4 text-center text-sm text-muted-foreground">
    <p>{t("noChecks")}</p>
    {!isFiltering && (
      <Link to="/orgs/$org/checks/new" search={{ …, group: group.slug, … }}
            data-testid="group-empty-new-check-link">
        {t("addCheckToGroup")}
      </Link>
    )}
  </div>
)}
```

That branch only renders when the group has **zero** rows. As soon as a group
has one check, the shortcut disappears: the user has to hit the page-level
"New Check" button ([checks.index.tsx:1422](web/dash0/src/routes/orgs/$org/checks.index.tsx:1422))
and then pick the group by hand in the form — or create the check ungrouped and
move it afterwards with the row's "Change Group" action.

This is backwards. Adding a second, third, tenth check to an existing group is
by far the more common action, and it is the one with no shortcut.

## Proposal

Put a persistent **"Add a check to this group"** action in the group *header*,
next to the existing move-up / move-down / edit icon buttons —
[checks.index.tsx:805-846](web/dash0/src/routes/orgs/$org/checks.index.tsx:805).

- A ghost icon button with the `Plus` icon (already imported at
  [checks.index.tsx:6](web/dash0/src/routes/orgs/$org/checks.index.tsx:6)),
  matching the sizing of its neighbours (`variant="ghost" size="icon"
  className="h-8 w-8"`), rendered `asChild` around a `<Link>` — same pattern as
  the existing `group-edit-button`.
- Placement: as the **first** item of the actions cluster, before the arrows, so
  the "create" action is not buried between reordering controls and edit. (If
  the implementer finds the arrows-then-plus order reads better in the live UI,
  either is acceptable — but keep the delete-style conventions: this is a
  neutral action, never destructive red.)
- Target: the same route + search params the empty-state link already builds —
  `/orgs/$org/checks/new` with `group: group.slug` and every other search key
  explicitly `undefined`. **Factor that search object into a small helper** (e.g.
  `newCheckSearchForGroup(slug)`) rather than duplicating the ~20-key literal in
  two places.
- Wrap it in a `Tooltip` (like the collapse toggle and the escalation
  indicator), and give it `aria-label` + `data-testid="group-add-check-button"`.
- The header row is itself a click target that toggles collapse; the actions
  cluster already calls `e.stopPropagation()` at
  [checks.index.tsx:807](web/dash0/src/routes/orgs/$org/checks.index.tsx:807),
  so a button added inside it must **not** collapse/expand the group when
  clicked. Verify this — it is the easy thing to get wrong.
- Show it regardless of `collapsed` (the header is always visible) and
  regardless of `isFiltering` — unlike the empty-state link, which hides while
  filtering because "no matches" ≠ "empty group". A header action is not making
  that claim, so it stays.

Keep the empty-state link as-is; it is still useful signposting for a brand-new
group.

### Strings

Reuse the existing `addCheckToGroup` key (`checks.json:131`) for the tooltip and
`aria-label` — it already reads correctly as an action label. Translate nothing
new unless a shorter tooltip form is preferred, in which case add the key to
**all four** locale files: `en`, `fr`, `de`, `es`
(`web/dash0/src/locales/*/checks.json`).

### Ungrouped bucket

`UngroupedChecksSection` ([checks.index.tsx:916](web/dash0/src/routes/orgs/$org/checks.index.tsx:916))
is a different shape (no group uid, no reorder controls) and is out of scope —
the page-level "New Check" button already creates ungrouped checks by default.

### Tests

Add a Playwright case to
[web/dash0/e2e/check-groups.spec.ts](web/dash0/e2e/check-groups.spec.ts):
with a group that **already has at least one check**, click
`group-add-check-button`, and assert (a) the URL is `/checks/new` carrying
`group=<slug>`, (b) the new-check form has that group preselected, and (c) — a
negative control — that clicking the button did not toggle the group's
`aria-expanded`.
