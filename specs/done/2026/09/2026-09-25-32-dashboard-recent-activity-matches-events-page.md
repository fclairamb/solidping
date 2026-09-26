---
model: sonnet
effort: medium
---

# The dashboard's "Recent activity" card renders events differently from the Events page

## Problem

The same events look different depending on where you see them. The org
dashboard (`/d/orgs/$org`) and the Events page (`/d/orgs/$org/events`) use two
separate renderers that have drifted apart:

| | Dashboard "Recent activity" | Events page |
|---|---|---|
| Component | `RecentActivityList` in `web/dash0/src/components/dashboard/dashboard-page.tsx:1090` | table in `web/dash0/src/routes/orgs/$org/events.tsx:147` |
| Icon | `getEventIcon()` (`event-display.tsx:195`): the Slack/Teams **emoji** registry (🔒 for a login) plus an older family fallback (colored `Cpu`, purple `Rocket`…) | `EventTypeLabel` (`event-display.tsx:410`): `EVENT_TYPE_MARKS` lucide icons (`LogIn` for a login), quiet by default, colored only for incident/security events, bold for loud ones |
| Label tone | always foreground | muted for routine events, foreground for colored ones |
| Time | `formatRelative()` (`dashboard-page.tsx:109`), bare `37s` / `3h`, right-aligned, no tooltip | `DurationAgo` (`il y a 52s`), left column, exact timestamp on hover |
| Actor | not shown | user name / "Système" with `User` / `Cpu` icon (`getEventActorName`) |
| Related check | blue `text-primary` link under the label (`HTTP — 1.1.1.1`) | `Cpu` icon + foreground link in a "Related" column |
| Related incident | blue "Incident" text link | mono pill with `AlertTriangle` |
| Row stripe | none | red/green leading stripe via `getEventRowStripe` |

The Events page is the rendering we want (it is the newer one, built on
`EVENT_TYPE_MARKS`, whose comment already says it is the dash0 icon identity
and that the emoji stay the chat-message identity). The dashboard is still on
the emoji path, so a login shows a padlock emoji on the home page and a
`LogIn` icon one click away.

## Proposal

Make the dashboard card render events exactly like the Events page, and stop
the two from drifting again by sharing one component.

1. **Extract a shared renderer** from `events.tsx` into
   `web/dash0/src/components/dashboard/` (e.g. `event-log-table.tsx`,
   `EventLogTable({ org, events })`): the Time / Event / Actor / Related table,
   with `DurationAgo` + hover timestamp, `EventTypeLabel`, the actor cell,
   the related check/incident links and `getEventRowStripe`. The Events page
   uses it unchanged, so its output must not move.
2. **Use it in `RecentActivityList`** inside the existing `Card` (keep the
   "Recent activity" title, the empty state, `SectionError`, the 8-event
   `size`, and the `recent-activity-footer` link to the Events page).
   The card sits inside `CardContent`, so drop the table's own outer
   border/shadow in that context (a `variant`/`className` prop) rather than
   nesting a bordered box in a card.
3. **Keep the activation descriptions.** The dashboard currently shows a
   second line for `org.activation.*` events ("Votre premier contrôle de
   disponibilité est actif et surveille.", and the first-notification channel
   link via `getEventChannelName` / `getEventChannelUid`). The Events page
   drops them. Render them in the shared component's Event cell, under the
   label, so both pages gain them rather than the dashboard losing them.
4. **Clean up.** Remove `formatRelative` and the `getEventIcon` import from
   `dashboard-page.tsx` if nothing else there uses them. `getEventIcon`
   itself stays: the incident timeline (`incidents.$incidentUid.tsx:1114`)
   still uses it. Whether that timeline should also move to
   `EVENT_TYPE_MARKS` is out of scope here.
5. **Mobile.** The table already scrolls horizontally inside
   `overflow-x-auto`. Check the dashboard card at 375px width: the Event and
   Time columns must stay readable without the page itself scrolling
   sideways.

## Tests

- `web/dash0/e2e/dashboard.spec.ts` has three recent-activity tests (around
  lines 256, 274 and 700). They look up the check link by name and the
  incident link by the name "Incident", and the activation description by
  text. Keep those accessible names and texts, and update the tests only
  where the markup legitimately changed.
- Add an assertion that a dashboard activity row and the matching Events page
  row show the same icon (e.g. both render the `EventTypeLabel` for
  `auth.login_succeeded` / `check.created`), so a future divergence fails.
- `bun run test:unit` for `event-display.test.ts`, plus the new component if
  it gets unit tests.

## Open questions

- Should the dashboard card show the Actor column? It is part of "same
  rendering", so the proposal keeps it. If the card feels too wide on
  laptop screens, hiding Actor below `md` is the fallback, not dropping it.

## Resolved open questions

- **Should the dashboard card show the Actor column?** Yes. Keep the Actor column so the card
  renders the same as the Events page, and hide it below the `md` breakpoint so the card does not
  get too wide on narrow screens. Never drop it.
