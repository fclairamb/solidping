# Escalation policy step targets — replace UID input with proper selectors

## Goal

Replace the free-text "Target UID" input in the escalation-policy step
editor with type-aware selectors. When the operator picks a target type
(`schedule`, `user`, `connection`), the second control should become a
dropdown listing the matching entities by name, not a UUID textbox.

## Why

Current UX (`/dash0/orgs/<org>/escalation-policies/<slug>`):

```
[Step 1]  Delay (min): [0]
[On-call schedule ▾]  [Target UID ____________]
```

Operators have no realistic way to know an on-call schedule's, user's, or
channel's UID. They'd have to open another tab, navigate to the entity,
copy the UID from the URL or an inspector, and paste it back. This is the
kind of internal-id leakage we already eliminated everywhere else (slugs in
URLs, named badges in tables) and it's the only spot that still asks
operators to think in UUIDs. It's also a footgun: a typo silently produces
an unresolvable target that fails at paging time, not at form-save time.

## Scope

### In scope

The step editor in
`web/dash0/src/routes/orgs/$org/escalation-policies.$slug.tsx` (and the
matching create page `escalation-policies.new.tsx` if it has the same
control — verify).

For each `target.type`:

- `all_admins` — no second control (unchanged).
- `schedule` — replace input with a `<Select>` populated from
  `useOnCallSchedules(org)`. Display each schedule's `name`, value is
  `schedule.uid`. Order alphabetically. Empty state: "No on-call schedules
  yet — [create one →]" linking to `/on-call/new`.
- `user` — replace input with a `<Select>` populated from `useMembers(org)`.
  Display `name` (fallback `email`), value is `member.userUid`. Order
  alphabetically by display name.
- `connection` — replace input with a `<Select>` populated from
  `useConnections(org)`. Display `name` with a `<ChannelIcon>` prefix,
  value is `connection.uid`. Optionally filter to `enabled` channels only,
  but show disabled ones grayed-out so existing references stay editable.

When the user changes target type, clear `targetUid` (existing behavior)
so the new selector starts unselected.

When loading an existing policy, the `targetUid` already on the step must
resolve to the matching option in the dropdown. If the referenced entity
no longer exists (deleted), surface a visible warning ("Schedule no longer
exists — pick another or remove this target") rather than silently showing
an empty select.

### Out of scope

- Inline create from the selector (e.g. "+ New schedule" menu item) —
  defer; for v1 we link out from the empty state only.
- Reordering or multi-select within a single step's targets — separate
  concern.
- Backend validation tightening (the API already validates target
  references; we're only changing the UI).
- `escalation-policies.new.tsx` only inherits this if it has the same
  step editor; if create routes through the same step component, we get
  it for free. Verify and extract a shared `<EscalationStepTargetEditor>`
  component if not already shared.

## Implementation notes

- Use the design-system `<Select>` (`@/components/ui/select`), not the raw
  `<select>` currently in use in this file. While we're touching the type
  dropdown, migrate it to `<Select>` too for visual consistency — the
  current `<select>` is the only raw HTML select left in this editor.
- Hooks already exist:
  - `useOnCallSchedules(org)` in `@/api/hooks`
  - `useMembers(org)` in `@/api/hooks`
  - `useConnections(org)` in `@/api/hooks`
- All three are already cached by react-query, so opening the dropdown
  three times (one per step) doesn't refetch.
- Loading state: while any of the three lists are loading, render a
  disabled select with a "Loading…" placeholder. Don't block save.
- Extract the per-target row into a small component
  (`<StepTargetRow step={...} index={...} onChange={...}>`) — the inline
  inline-update closures are hard to read once the selectors are wired in.

## Acceptance criteria

- Editing an existing policy with a `schedule` target shows the schedule's
  name pre-selected; switching the dropdown updates the saved UID.
- Same for `user` (member name) and `connection` (channel name + icon).
- Selecting `all_admins` hides the second control entirely.
- Saving a policy with each target type round-trips through the API and
  pages correctly (verify with the existing escalation paging test, or
  add a smoke test that creates a policy with each type).
- A policy referencing a deleted target renders a warning state instead
  of a blank select.
- The raw `<select>` for target type is replaced with `<Select>`.
- `escalation-policies.new.tsx` benefits from the same fix (either via a
  shared component or a parallel edit) — verified manually.
- Playwright e2e: at least one test that creates a policy via the UI,
  picks a real on-call schedule from the dropdown, saves, and asserts
  the policy detail page renders the schedule's name (not a UID).

## Files affected

- `web/dash0/src/routes/orgs/$org/escalation-policies.$slug.tsx`
- `web/dash0/src/routes/orgs/$org/escalation-policies.new.tsx` (verify)
- Likely a new
  `web/dash0/src/components/escalation/step-target-row.tsx` shared
  between create and edit
- Translation keys under `escalation:targets.*` and
  `escalation:editor.*` may grow (e.g. `editor.targetMissing`)
- `web/dash0/e2e/escalation-policies.spec.ts` (or equivalent)
