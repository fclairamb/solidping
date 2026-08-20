---
model: sonnet
effort: medium
---

# The "SMS & voice" panel crowds the top of the integrations page — move it to the bottom, collapsed by default

## Problem

On `/dash0/orgs/default/integrations`, the "SMS & voice" mode panel
(`SMSModePanel`, [sms-mode-panel.tsx](web/dash0/src/components/integrations/sms-mode-panel.tsx))
is rendered **above** the integrations list
([integrations.index.tsx:175](web/dash0/src/routes/orgs/$org/integrations.index.tsx:175)),
always fully expanded. It is a tall, mostly-static explainer card (~5 paragraphs
plus a button in the "unavailable" state), so the page's actual content — the
org's integrations — gets pushed below the fold by contextual help the admin
reads once.

Requested behavior: the block should sit **at the bottom of the page** and be
**collapsed by default**.

## Proposal

In [integrations.index.tsx](web/dash0/src/routes/orgs/$org/integrations.index.tsx):

- Move the `<SMSModePanel …>` render below the list/empty-state block (after the
  `showEmptyState ? … : …` section), so it is the last element of the page in
  both the empty and populated states. Update the comment at
  [integrations.index.tsx:171](web/dash0/src/routes/orgs/$org/integrations.index.tsx:171)
  accordingly — the "rendered above the list" rationale no longer holds.

In [sms-mode-panel.tsx](web/dash0/src/components/integrations/sms-mode-panel.tsx):

- Make the card body collapsible, collapsed by default. Reuse the existing
  collapsed-card pattern — the design reference already ships
  `CollapsibleSection` ([collapsible-section.tsx](web/dash0/src/components/ui/collapsible-section.tsx),
  Radix `Collapsible` underneath). Either wrap the panel content in
  `CollapsibleSection`, or apply the same `Collapsible`/`CollapsibleTrigger`
  pattern inside the existing `Card` if the section chrome doesn't fit —
  whichever reads closest to the design reference. If a "collapsible card"
  variant ends up being a new primitive, add it to
  [design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx)
  per the repo convention.
- **The mode badge must stay visible while collapsed.** The whole point of the
  panel is that "SMS works via the instance" vs "SMS unavailable" is invisible
  otherwise; the collapsed header should still carry the title ("SMS & voice")
  and the `sms-mode-badge` badge ("Your own account" / "Provided by this
  instance" / "Not available") — matching `CollapsibleSection`'s
  "collapsed sections are compressed, not hidden" philosophy (its `summary`
  slot fits this).
- Expanding reveals the existing explanation paragraphs, the voice line, and
  the "Use my own Twilio account" button — no copy changes.

Tests:

- Update [e2e/sms-mode.spec.ts](web/dash0/e2e/sms-mode.spec.ts): assertions on
  `sms-mode-explanation` / `sms-add-own-twilio` now need to expand the panel
  first; add an assertion that the panel renders collapsed by default with the
  badge visible, and (cheaply, e.g. via bounding-box or DOM order) that it sits
  after the integrations list.

Open question (minor): whether the collapsed state should persist or auto-expand
in the "unavailable" mode — the spec takes the request literally (always
collapsed by default, all modes); revisit only if the E2E flow shows the
"unavailable" call-to-action becomes too buried.
