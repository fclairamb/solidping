---
model: sonnet
effort: medium
---

# The "Getting started" block's items need distinct background colors and visual polish

## Problem

Follow-up on the getting-started card (dashboard onboarding checklist,
[onboarding-checklist.tsx](web/dash0/src/components/dashboard/onboarding-checklist.tsx)).
The report, verbatim: *The "Getting started" block should have a different
background colors. and possibly other changes to make them nicers* — "them"
being the step rows.

Current styling:

- The card is tinted `border-primary/30 bg-primary/5`
  ([onboarding-checklist.tsx:249-252](web/dash0/src/components/dashboard/onboarding-checklist.tsx:249)).
- Each step row is `rounded-md border p-3 hover:bg-muted/50` with **no
  background of its own**
  ([onboarding-checklist.tsx:325](web/dash0/src/components/dashboard/onboarding-checklist.tsx:325)),
  so the card's primary tint bleeds through every row. Rows read as outlines
  drawn on the tint rather than as distinct items, and done rows look exactly
  like pending ones apart from the tick and strikethrough.

## Proposal

Visual polish of the checklist rows, in both themes, without touching behavior:

1. **Explicit row backgrounds.** Give each step row its own background so it
   reads as a crisp item sitting on the tinted card — e.g. `bg-card` (or
   `bg-background`) as the base. Keep a visible hover state on top of it.
2. **Differentiate done from pending.** A subtle state-carrying background —
   e.g. done rows get a faint emerald tint (consistent with the existing
   emerald tick and the all-set banner at
   [onboarding-checklist.tsx:276](web/dash0/src/components/dashboard/onboarding-checklist.tsx:276)),
   pending rows keep the neutral base. Use the same
   light/dark-safe pattern already in the file (`emerald-500/10` +
   `dark:` variants) or theme tokens; never a raw color that breaks dark mode.
3. **Optional adjacent niceties** (small, in the same spirit — pick what
   genuinely improves the card, skip what doesn't):
   - The done `statusPage` row's CTA still reads "Create a status page"; a
     done-state label ("View status pages") would read better. The `check`
     step already uses a state-neutral "View checks".
   - The `alerts` row's CTA is forced to `outline` even when no
     "Send me a test alert" button is present
     ([onboarding-checklist.tsx:395](web/dash0/src/components/dashboard/onboarding-checklist.tsx:395)) —
     when the test button is absent, the first pending step could carry the
     primary variant like the other pending rows.
4. **Constraints.**
   - Keep all `data-testid` attributes and the row's stretched-link click
     target intact — Playwright suites and the whole-row click behavior depend
     on them.
   - Verify both themes (the card renders on the dashboard and, with fixture
     props, in the design reference).
   - Update the design-reference section
     ([design-reference.tsx:5460](web/dash0/src/routes/orgs/$org/design-reference.tsx:5460))
     so it shows the new look — it is the canonical catalog.
   - Mobile layout (stacked rows) must stay intact.
   - New locale keys, if any (e.g. a done-state CTA label), go into all six
     locales — `bun run test:unit` enforces this.

## Open questions

- The report could also mean the **card's own** tint (`bg-primary/5`) should
  change, not only the rows. Default here: keep the card tint (it
  distinguishes the block from regular dashboard cards) and fix the rows; if
  the card color itself is the complaint, that is a one-line follow-up.
- "Nicer" is taste: the implementer should eyeball the result in the running
  app (light + dark) rather than trusting class names, and keep changes
  restrained — this is polish, not a redesign.
