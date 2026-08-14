---
model: sonnet
effort: medium
---

# Relative incident timestamps ("46m ago") hide the absolute time needed for incident analysis

## Problem

Incident surfaces render only relative times. That reads nicely at a glance, but
during incident analysis it is actively harmful: correlating an incident with
logs, metrics, deploys or an external provider's status page requires the exact
timestamp, and "46m ago" forces the operator to do mental clock arithmetic
(and gets stale the moment the page stops re-rendering).

Current call sites:

- `web/dash0/src/routes/orgs/$org/incidents.index.tsx:90-104` — a local
  `formatRelativeTime()` helper, rendered at `:290` **inside the row `<Link>`**
  that navigates to the incident detail page.
- `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:430` (status
  updates timeline) and `:576` (comments) — `formatDistanceToNow` from
  `date-fns`.
- Same pattern exists on the jobs pages (`jobs.index.tsx:125`,
  `jobs.$jobUid.tsx:55`, `jobs.check.$checkJobUid.tsx:50`) — same fix applies
  cheaply once the shared component exists.

There is no way to see the absolute time (no `title` attribute, no tooltip),
and no way to copy it.

## Proposal

Add a shared **`<TimeAgo>`** primitive in `web/dash0/src/components/ui/` and
swap it in at the call sites above. Behavior:

1. **Renders the relative string** exactly as today (keep the existing
   `formatRelativeTime` tiering: `just now` / `Nm` / `Nh` / `Nd` / locale date
   past 30 days).
2. **Hover shows the absolute time** in a tooltip: both local time and UTC,
   e.g. `2026-08-14 11:31:07 (local) · 2026-08-14T09:31:07Z`. Use the design
   system's Tooltip primitive (check the design reference first); fall back to
   a native `title` only if no Tooltip primitive is shipped yet.
3. **Click copies the UTC timestamp** in ISO 8601 (`2026-08-14T09:31:07Z`) to
   the clipboard, with visible feedback (tooltip text flips to "Copied", or a
   transient check icon). ISO 8601 UTC is the right copy format: unambiguous,
   sortable, and paste-able into log queries.
4. **Event handling inside row links**: the incidents-list timestamp lives
   inside the row's `<Link>`. The click handler must `preventDefault()` +
   `stopPropagation()` so copying does not navigate — and the component's hit
   area must stay tight (just the text) so the rest of the row still navigates.
5. **Affordance**: a subtle dotted underline (`decoration-dotted`) +
   `cursor-pointer` on the timestamp so click-to-copy is discoverable; without
   a visual hint nobody will find it.
6. **Mobile**: there is no hover on touch devices, so the tap behavior *is*
   the mobile path — the "Copied" feedback should include the full absolute
   time so a tap both reveals and copies it. This keeps the page fully usable
   on mobile per repo convention.
7. **Incident detail page shows the absolute time inline** — analysis mostly
   happens there, and the operator wants to *compare several timestamps at
   once*; hovering each one is tedious. Give `<TimeAgo>` a display variant
   (e.g. `variant="inline"`) that renders `09:31:07 UTC · 46m ago` (locale
   date included when not today) instead of hiding the absolute time behind
   hover. Use the inline variant on the incident detail page (status-updates
   timeline, comments, started/resolved header fields); keep the compact
   tooltip variant for dense lists (incidents index, jobs). Click-to-copy and
   the tooltip (for the local-time complement) apply to both variants.
8. **Design reference**: add `<TimeAgo>` (both variants) to
   `web/dash0/src/routes/orgs/$org/design-reference.tsx` with its import line,
   per the mandatory design-reference convention.

Scope: incidents list + incident detail (updates timeline, comments, header)
are the required call sites; migrate the jobs pages too since they share the
exact pattern and the swap is mechanical. Other relative-time call sites
(sessions, tokens, email inbox) can adopt the component opportunistically
later.

## Notes

- Relative strings go stale between re-renders (a "46m ago" stays frozen).
  The list pages poll, so this is mostly masked; still, since the component
  is being built anyway, give `<TimeAgo>` a coarse ticking re-render (e.g.
  30s interval, single shared timer — not one interval per instance) so
  long-lived tabs don't drift.
- E2E: a Playwright test should hover the timestamp and assert the tooltip
  shows the absolute time, and click it and assert the clipboard contains the
  ISO 8601 UTC string (grant `clipboard-read` permission in the test context).
