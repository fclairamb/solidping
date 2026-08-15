---
model: sonnet
effort: medium
---

# Incident notification lists don't say which event was notified, and event types lack a consistent emoji + color identity

## Problem

The incident detail page's **Notifications** card ("Who was notified and the
delivery status" —
[incidents.$incidentUid.tsx:1340](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1340))
shows Time / Status / Target / Source / Channel — but **not which event the
notification was about** (`incident.created`, `incident.resolved`,
`incident.escalated`, `incident.reopened`, …). A resolved incident's card lists
several near-identical rows and you can't tell the "down" alert from the
"recovered" notice without clicking into each one. The API already returns
`eventType` on every row
([incidentnotifications/service.go:47](server/internal/handlers/incidentnotifications/service.go:47)),
so this is purely a display gap.

The notification detail page does show the event type, but as a raw code
string — `<code>{data.eventType}</code>` at
[notifications.$notificationUid.tsx:346](web/dash0/src/routes/orgs/$org/notifications.$notificationUid.tsx:346)
— with no human label, emoji, or tint.

More broadly, event types have no single visual identity. Today we have:

- [event-display.tsx](web/dash0/src/components/dashboard/event-display.tsx):
  `getEventIcon` (lucide icons, coarse per-family) and `getEventTone` (badge
  tint per family — all failure-ish incident events share one red, etc.), used
  by the events page, the dashboard, and the design reference.
- Chat surfaces hand-pick emoji per event ad hoc: msteamsbot.go uses 🔴/🟢/⚠️,
  Telegram uses ✅, etc. — with no shared mapping.

The ask: describe the event kind wherever notifications are listed, and give
each event type a **specific emoji and a specific color**, used consistently
everywhere these events are rendered.

## Proposal

**A. One canonical per-event-type registry (frontend).**

Extend [event-display.tsx](web/dash0/src/components/dashboard/event-display.tsx)
with a per-event-type map (falling back to today's family rules for unmapped
types) giving each event type:

- an **emoji** (e.g. `incident.created` 🔴, `incident.reopened` 🔁 (red),
  `incident.escalated` ⚠️, `incident.escalation_failed` ❌,
  `incident.resolved` 🟢, `incident.acknowledged` ✅,
  `incident.unacknowledged` ↩️, `incident.snoozed` 💤 — final picks at
  implementation time; where a chat surface already established an emoji for
  an event (🔴 down / 🟢 recovered / ⚠️ escalated in msteamsbot.go), reuse it
  rather than inventing a competing one),
- a **tone** (badge classes) — per-type where it matters (resolved emerald,
  created/reopened/escalation red family, acknowledged amber, snoozed
  neutral/blue…), still falling back to the family tone,
- the existing translated **label** (`getEventLabel` — labels stay in i18n;
  emoji live in the registry, never in translation strings).

Export a small `EventTypeBadge` (emoji + label + tone) component so every
surface renders the same thing, and add it to the design reference page
([design-reference.tsx:2460](web/dash0/src/routes/orgs/$org/design-reference.tsx:2460)
already documents `getEventTone` — extend that entry) per the repo's
design-reference rule. The emoji + tint are decoration layered on the label,
never the only signal (same accessibility rule the design reference already
states for `getEventTone`).

**B. Show the event in notification lists.**

- Add an **Event** column to the incident page's Notifications card
  ([incidents.$incidentUid.tsx:1340](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1340))
  rendering `EventTypeBadge` from `row.eventType`. Keep the table usable on
  mobile (the card already scrolls; don't add fixed widths).
- On the notification detail page, replace the raw
  `<code>{data.eventType}</code>`
  ([notifications.$notificationUid.tsx:346](web/dash0/src/routes/orgs/$org/notifications.$notificationUid.tsx:346))
  with the same `EventTypeBadge` (keep the raw type visible somewhere
  secondary, e.g. a title/tooltip, for debugging).

**C. Use the registry everywhere events are rendered in dash0.**

Switch the existing surfaces to the registry so they agree with the new
per-type colors: the events page
([events.tsx:177](web/dash0/src/routes/orgs/$org/events.tsx:177)), the
dashboard recent-events list
([dashboard-page.tsx:1080](web/dash0/src/components/dashboard/dashboard-page.tsx:1080)),
and the incident timeline on the incident detail page. Where a lucide icon is
structurally expected, the emoji may replace or accompany it — pick one
treatment and apply it consistently.

**D. Backend chat surfaces — align, don't rewrite.**

msteamsbot.go / Telegram / Slack already carry emoji; they are separate
codebases from dash0 and stay hand-authored, but their emoji choices must
match the registry's picks for the same events (adjust either side so one
event = one emoji product-wide). No functional changes to delivery.

**E. Tests.**

- Playwright: incident detail Notifications card shows the event badge per
  row; notification detail shows the labeled badge instead of the bare code
  string.
- Unit-level: registry returns a mapping for every `EventType*` constant
  defined in [event.go](server/internal/db/models/event.go) that dash0 can
  receive (guard against a new event type shipping without an identity —
  fallback path covers it, but the test documents the intended pairs).

## Open questions

- Exact emoji per event type — proposal above is a starting point; the
  implementer should finalize against what msteamsbot.go already ships and
  keep one emoji per event product-wide.

## Resolved open questions

**Exact emoji per event type** — **Decision:** the implementer finalizes the
picks; this needs no further input. Binding rules, in order:

1. Where a backend chat surface already ships an emoji for an event, reuse it
   rather than inventing a competing one — `msteamsbot.go` has already
   established 🔴 down / 🟢 recovered / ⚠️ escalated.
2. For everything else, use the Proposal's starting-point list as the final
   picks: `incident.created` 🔴, `incident.reopened` 🔁,
   `incident.escalated` ⚠️, `incident.escalation_failed` ❌,
   `incident.resolved` 🟢, `incident.acknowledged` ✅,
   `incident.unacknowledged` ↩️, `incident.snoozed` 💤.
3. Exactly one emoji per event type product-wide — where a backend surface and
   the registry disagree, change whichever side is the outlier so the pairing
   is identical everywhere.
