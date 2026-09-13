---
model: sonnet
effort: medium
---

# The Dependencies card stacks one full-width amber banner per warning above the list it is about

## Problem

The check detail page's **Dependencies** card
(`web/dash0/src/components/checks/dependencies-card.tsx:66`) mounts
`DependencyWarnings` (`web/dash0/src/components/checks/dependency-warnings.tsx:22-51`)
*above* the **Depends on** / **Depended on by** lists. That component renders one
amber `Alert` per warning — title plus a three-line body — so a check with five
hard parents that all share the same confirmation window gets five near-identical
banners stacked before the reader reaches the list they describe (see the
screenshot that prompted this: five "Confirms faster than … can detect" alerts,
then the five-row list underneath).

Three things are wrong with that:

- **It is not proportionate.** The lint is deliberately soft — the component's own
  comment says "Amber, never destructive, and never a blocking validation … the
  runtime confirmation hold already covers the gap". Five stacked warning boxes read
  as "this check is misconfigured", which is the opposite of the intended
  "nothing is broken, just FYI" tone.
- **The information is detached from its subject.** Each warning is keyed by
  `dependencyUid` (`web/dash0/src/api/hooks.ts:1228-1236`) and names its
  `parentCheck`, so it belongs to exactly one row of the **Depends on** list. Today
  the reader has to match banner N to row N by name; the row itself carries no hint
  that a warning exists.
- **The row list does not stand off from the card.** `DependencyRowList` tints its
  rows `bg-muted/30` (`web/dash0/src/components/checks/dependency-row.tsx:33-36`),
  but on the dark theme that is nearly indistinguishable from the `Card` behind it
  (`web/dash0/src/components/ui/card.tsx:14`: `bg-card` plus a faint
  top-lit gradient). In the screenshot the rows only read as a list because of
  the `divide-y` hairlines; the "one bordered container tinted a step off the panel
  behind it" the design reference promises
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx:4727`) is not visibly
  happening.

## Proposal

Move each warning *into* the row it is about, shrink it to an info glyph with the
explanation on demand, and give the row list a background that clearly steps off
the card.

### 1. Per-row inline warning

- `DependenciesCard` builds a `Map<dependencyUid, DependencyWarning>` from
  `deps?.warnings` and passes the matching warning (if any) down to each
  **Depends on** row. Warnings only ever concern hard `dependsOn` edges, so the
  **Depended on by** list is untouched.
- `DependencyRow` (`dependency-row.tsx:64-92`) gains an optional `hint?: ReactNode`
  slot rendered right after the `kind` badge (same column, `inline-flex`,
  `gap-1.5`), so the badge and the glyph read as one cluster: `● Hard ⓘ`.
- The glyph is a small amber `Info` icon from lucide (`h-3.5 w-3.5`,
  `text-amber-600 dark:text-amber-400` — the same amber family the `warning`
  `Alert` variant uses, so the semantic colour is unchanged), wrapped in a
  `Tooltip` whose content is the existing
  `dependencies:warnings.confirmationMargin.title` on one line and `.body`
  below it, exactly as the banner rendered them. Reuse the locale keys as they
  are — the `dependency-warnings.test.ts` parity test keeps guarding all four
  locales.
- **Mobile:** the page must stay usable on touch, and a hover tooltip is not.
  The trigger is a real `<button type="button">` (44px touch target via padding,
  `aria-label` = the warning title) and the explanation opens on tap as well as
  hover. Use `Tooltip` for pointer devices and fall back to `Popover` on
  coarse-pointer / touch (or a single `Popover` that also opens on hover if that
  is simpler — implementer's call, but tap-to-open must work). The
  design-reference already ships both primitives (`design-reference.tsx:157-166`,
  `:4104-4114`).
- Keep the row's existing `data-testid` layout; add `data-testid="dependency-warning-hint"`
  on the trigger so e2e can locate it inside `depends-on-list`.
- Delete the stacked-banner mount at `dependencies-card.tsx:66`. `DependencyWarnings`
  itself can either be deleted or reduced to the new glyph component; whichever
  it is, its design-reference entry (`design-reference.tsx:3835-3867`, "Dependency
  warnings") must be rewritten to show the new inline form, since that page is
  the canonical catalogue and must not keep advertising the banner.

### 2. Row-list background

- In `DependencyRowList` (`dependency-row.tsx:24-42`) make the `muted` tone
  visibly different from the card on both themes. Something in the spirit of
  `bg-muted/60 dark:bg-white/[0.035]` plus a slightly stronger border
  (`dark:border-white/10` already matches `Card`); tune by eye against
  `http://localhost:4000/d/orgs/default/design-reference#dependency-row` in
  light and dark until the container reads as a distinct inset panel, not a
  set of hairlines.
- `DependencyRow`'s `interactive` hover (`hover:bg-muted/50`) must still be
  perceptible on top of the new base tint — bump it if needed.
- The change is in the shared primitive, so the check form's Dependencies section
  (`web/dash0/src/components/checks/form/sections/dependencies.tsx`) picks it up
  too; check it there as well.
- Update the "Dependency row" section description in the design reference so it
  stops describing the old value and states the new one.

### 3. Tests

- **Unit** (`dependency-warnings.test.ts`): keep the four-locale key parity test;
  point it at whichever component now owns the strings.
- **e2e** (`web/dash0/e2e/check-dependencies.spec.ts`): the suite currently
  never exercises a warning at all (no test mentions `dependency-warnings`). Add
  one: create a parent with a long-enough confirmation window and a child with a
  short one, link them hard, open the child's detail page, assert
  `depends-on-list` contains exactly one `dependency-warning-hint`, that it sits
  in the parent's row (not a sibling row without a warning), that no amber
  banner exists above the list, and that clicking the hint reveals text
  containing "Confirms faster than". Include a negative control: a second soft
  or well-margined parent row has no hint.
- Frontend lint: no *new* eslint errors (the base is already red on
  react-hooks debt; do not fix that inline).

### Out of scope

- No backend change. The API shape (`warnings[]` keyed by `dependencyUid`) is
  already exactly what the inline form needs.
- No change to the check form's edit-side rendering of warnings — it does not
  render them today and this spec does not add them there.
