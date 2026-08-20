---
model: sonnet
effort: medium
---

# status0: incident updates render as a card-inside-a-card, and the incident block touches "Core services"

## Problem

On the public status page (status0), an active incident renders badly in two ways
(seen with one open incident, "Acme API is experiencing issues"):

1. **Double-boxed updates.** The incident card
   ([active-incidents.tsx:127-131](web/status0/src/components/shared/active-incidents.tsx))
   renders its updates through `StatusUpdateThreadList`, and each entry is a
   `StatusUpdateCard` whose root carries its own full card chrome —
   `rounded-lg border border-border bg-card p-4`
   ([status-update-card.tsx:83](web/status0/src/components/shared/status-update-card.tsx)).
   The result is a bordered white card floating inside the already-bordered,
   already-surfaced incident card: a block inside a block. The update (title,
   kind badge, body) should sit directly in the parent incident block — no inner
   border, no inner background, no inner rounding.

2. **No breathing room before the next section.** The `ActiveIncidents` section
   ([status-page-view.tsx:485](web/status0/src/components/shared/status-page-view.tsx))
   carries only its own `mt-6`; the sections grid that follows (`<div
   className="space-y-6">`, line 488, which renders "Core services" etc.) has no
   top margin, so the incident card and the first section card sit flush against
   each other. There should be the same vertical gap between the incident block
   and "Core services" as between other page sections (`6` / 1.5rem).

The same double-box pattern also appears lower on the page: the "Recent
updates" timeline wraps incident-linked updates in an "Incident thread" bordered
section ([status-updates-timeline.tsx:108-123](web/status0/src/components/shared/status-updates-timeline.tsx))
and then renders each update as a fully-chromed `StatusUpdateCard` inside it —
box inside box again.

## Proposal

1. **Give `StatusUpdateCard` an embedded/plain variant** (e.g. a
   `variant?: "card" | "plain"` prop, default `"card"`), where `"plain"` drops
   the `rounded-lg border border-border bg-card` chrome (and the `p-4`, since
   the wrapping `StatusUpdateThreadList` row already pads with `p-4`) but keeps
   everything else — id/scroll anchor, header row, badges, hardened Markdown
   body, "Read more" link, and all `data-testid` hooks. Do **not** fork the
   component: the hardened Markdown renderer (skipHtml + link-scheme allowlist)
   must stay in exactly one place, per the existing comments in
   [active-incidents.tsx:31-37](web/status0/src/components/shared/active-incidents.tsx)
   and [status-updates-timeline.tsx:67-75](web/status0/src/components/shared/status-updates-timeline.tsx).

2. **Use the plain variant everywhere the card is nested in an
   already-bordered container**: pass it through `StatusUpdateThreadList` so
   both call sites benefit — the active-incident card and the "Incident thread"
   block in the recent-updates timeline. The `divide-y divide-border` separators
   in `StatusUpdateThreadList` already give multi-update threads their internal
   structure. Standalone updates in `StatusUpdatesTimeline` (the
   `type === "standalone"` branch) keep the default `"card"` variant — they have
   no parent box.

3. **Restore the section gap**: make the gap between `ActiveIncidents` and the
   sections grid match the page's inter-section rhythm — e.g. add `mb-6` to the
   `ActiveIncidents` `<section>` (it renders `null` when there are no incidents,
   so the margin disappears with it) or an equivalent spacing fix at the
   [status-page-view.tsx:485-488](web/status0/src/components/shared/status-page-view.tsx)
   level. Verify the healthy-page layout (no incidents) is unchanged.

Verification: with the dev server up (`make dev-test`), open an org status page
with one active incident carrying at least one update, and confirm (a) the
update text sits directly in the incident block with no inner border/box, (b)
there is a clear gap before "Core services", (c) the recent-updates incident
thread no longer double-boxes, and (d) existing status0 E2E/Playwright tests
still pass — the `data-testid` attributes must not change.
