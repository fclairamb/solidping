---
model: sonnet
effort: medium
---

# The Getting Started checklist blends into the dashboard and its step rows are only clickable on the tiny CTA button

## Problem

The dashboard's Getting Started block (the onboarding checklist,
`web/dash0/src/components/dashboard/onboarding-checklist.tsx`) has two UX
shortcomings:

1. **It looks like every other dashboard card.** `OnboardingChecklistCard`
   renders a plain `Card` (`onboarding-checklist.tsx:255`) with the default
   border/background, so the one card that is actively asking the user to do
   something is visually indistinguishable from the KPI and activity cards
   around it. It should stand out.

2. **Each step row is mostly dead surface.** A step row
   (`OnboardingStepRow`, `onboarding-checklist.tsx:313-390`) is a wide
   bordered box, but the only clickable part is the small CTA button on the
   right (`OnboardingStepLink`, `onboarding-checklist.tsx:392-439`). Clicking
   the row itself — the icon, the title, the description, the empty space —
   does nothing. The whole row should navigate exactly like its CTA button.

## Proposal

All changes are in `web/dash0/src/components/dashboard/onboarding-checklist.tsx`
(plus the design reference and e2e coverage).

### 1. Distinct card tint

Give the card a subtle accent treatment that separates it from neutral
dashboard cards — e.g. a primary-tinted border and background wash
(`border-primary/30 bg-primary/5` or an equivalent token-based tint), applied
on the `Card` at `onboarding-checklist.tsx:255`. Constraints:

- Must work in both light and dark themes (use theme tokens / opacity tints,
  not hard-coded light-only colors).
- Must not collide with the existing semantic tints already used inside the
  card (the emerald "all set" banner at `onboarding-checklist.tsx:277-289`)
  or with the status colors (green/yellow/red) that mean up/degraded/down
  elsewhere on the dashboard — so avoid green/amber/red for the card chrome.
- Keep the mandatory design-reference pass: the card is already cataloged in
  `web/dash0/src/routes/orgs/$org/design-reference.tsx:5327` — the tinted
  version renders there automatically via `OnboardingChecklistCard`, but
  update the section's description text if it mentions the card's look.

### 2. Whole step row is clickable

Make each step row act as one large click target with the same effect as its
CTA:

- Clicking anywhere on the row navigates to the step's target route — the
  same `to`/`params`/`search` the CTA link builds (including the
  status-page special case that pre-attaches `firstCheckUid`,
  `onboarding-checklist.tsx:408-423`).
- Nested interactive elements must keep working independently: the
  "Send test alert" button on the alerts row
  (`onboarding-checklist.tsx:367-381`) must trigger only the test alert, not
  the navigation. Use `stopPropagation` on the inner buttons or the
  stretched-link pattern (row `relative`, link `after:absolute after:inset-0`,
  inner buttons `relative z-10`) rather than nesting `<a>` inside `<a>` —
  nested anchors are invalid HTML and break keyboard/screen-reader semantics.
- Add a hover affordance so the enlarged target is discoverable:
  `cursor-pointer` plus a hover background (e.g. `hover:bg-muted/50`) on the
  row.
- Keyboard accessibility: the row's navigation must remain reachable and
  announced as a single link (the existing CTA link can serve as the
  accessible link that is stretched over the row; don't add a second
  tab stop for the same destination).
- Done steps keep their (outline) CTA today — the row click should mirror it
  the same way, done or not.

### Tests

- Extend `web/dash0/e2e/onboarding-checklist.spec.ts`: clicking a step row's
  body (not the CTA button) navigates to the step's route; clicking the
  test-alert button on the alerts row fires the test alert without
  navigating away from the dashboard.
- Keep existing selectors (`onboarding-step-*`, `onboarding-step-*-cta`)
  working; `web/dash0/e2e/magic-wand.spec.ts` also touches
  `onboarding-step` test ids — don't break it.

## Open questions

- Exact tint: primary-blue wash is the default suggestion; if the design
  reference already defines a "callout/highlight" card treatment, reuse it
  instead of inventing a new one.
