---
model: opus
effort: high
---

# A status-page incident publication can outlive its resolved incident forever, so the TV board says "degraded" while every check is up

## Problem

Reported on the dev instance: the dash0 checks list filtered on
`status=down,validating,warning,created` for the `acme` org is empty, yet the
public wallboard at `/status0/acme/acme/tv` shows **"Some Systems Degraded"**.

### What the live data says

| Source | Value |
|---|---|
| `GET /api/v1/orgs/acme/checks` (all 436, paged by cursor) | every check is `up` |
| `GET /api/v1/orgs/acme/checks?status=down,validating,warning,created` | `total: 0` |
| `GET /api/v1/status-pages/acme/acme` → `overallStatus` | `operational` |
| `…` → `statusCounts` | `{operational: 200, degraded: 0, down: 0, maintenance: 0, unknown: 0}` |
| `…` → `activeIncidents` | **1** — `"Some services are experiencing issues"`, `state: identified`, `severity: minor`, `startedAt: 2026-08-23T12:17:51Z`, no updates |
| Org-side publication record | `autoCreated: false`, `humanTouched: true`, `updatedAt: 2026-08-23T12:18:21Z` |
| Linked internal incident | `"acme.com (http) is down"`, `state: resolved`, `startedAt 11:41:36Z`, **`resolvedAt 12:17:22Z`** |
| Page `autoResolve` | `if_untouched` (the default) |

So the checks are genuinely fine. The banner is driven by an incident
**publication** that has been open on the public page for ten days, linked to
an internal incident that resolved **29 seconds before the publication was
created**.

### The chain that produces the headline

1. The TV board takes the worse of the server rollup and an incident-derived
   floor — `resolveTvState()` in
   [`web/status0/src/lib/tv-board.ts:71`](../../web/status0/src/lib/tv-board.ts).
   `severityFloor()` (`tv-board.ts:34`) maps `major`/`minor`/*unset* → `degraded`,
   `critical` → `down`. Any non-resolved publication therefore floors the board
   at "degraded" — this is intentional and documented in
   `specs/done/2026/08/2026-08-29-08-status-page-wallboard-tv-mode.md:74`
   ("manually published incidents must show").
2. The ordinary scrolling status page reads `overallStatus` only
   (`web/status0/src/components/shared/status-page-view.tsx:86`); open
   publications tint the banner but do not change its words. So `/status0/acme/acme`
   says "All Systems Operational" while `/tv` says "Some Systems Degraded" —
   the two public surfaces disagree with each other, not just with dash0.
3. The server rollup (`models.RollupPageStatus`,
   [`server/internal/db/models/page_status.go:59`](../../server/internal/db/models/page_status.go))
   is a pure function of raw check status + maintenance. It knows nothing about
   publications, and dash0's checks list knows nothing about publications either.
   Nothing in dash0 tells an operator "your public page has an open incident
   that no check backs any more".

### Why the publication never closed

- `PublishIncident()` in
  [`server/internal/handlers/incidentpublications/service.go:631`](../../server/internal/handlers/incidentpublications/service.go)
  never looks at `incident.State`. Publishing an **already-resolved** incident
  mints a fresh publication in the opening state (`investigating`), with a
  templated "we are investigating" update, as if the outage were live.
- The only thing that ever auto-closes a publication is
  `OnIncidentResolved()` ([`policy.go:289`](../../server/internal/handlers/incidentpublications/policy.go)),
  which fires once, at the moment the incident resolves. Here it fired at
  12:17:22, found no publication, and will never fire again for that incident.
- Even if the timing had been the other way round, two more rules would have
  kept it open:
  - the resolve loop skips hand-published entries outright:
    `if !pub.AutoCreated || pub.IsResolved() { continue }` (`policy.go:329`);
  - `PublishIncident()` stamps `HumanTouchedAt = now` on creation
    (`service.go:678`), so under `if_untouched` the publication counts as
    "a human owns the narrative" before any human has written a word — the
    policy would post a `monitoring` note and leave it open (`policy.go:364`).

  Net effect: **for a publication created via "Publish to status page", the
  `if_untouched` default behaves exactly like `never`.** The operator has to
  remember to come back and resolve by hand, and nothing reminds them.

### Why the operator didn't see it

- dash0's checks list is the page people look at when the TV goes amber, and
  it has no notion of publications.
- The TV board does render an `ActiveIncidentCard` for the publication
  (`web/status0/src/components/tv/tv-board.tsx:172`), but the headline itself
  gives no hint that the amber comes from a publication rather than a check,
  and the "affected services" list is empty because `failingResources()`
  (`tv-board.ts:309`) only looks at check status.

## Proposal

Fix the lifecycle so this state cannot arise silently, then make it visible
wherever it still can.

### 1. Publishing a resolved incident yields a resolved publication

In `PublishIncident()` (and the generic `Create()` when `incidentUid` points at
a resolved incident): if `incident.State == resolved`, create the publication
**already resolved** — `PublicState = resolved`, `ResolvedAt = incident.ResolvedAt`,
`PublishedAt = now` — and post the templated `investigating` *and* `resolved`
updates back-to-back (the existing `templatesFor(page.Language)` strings),
so the public timeline reads as a retroactive post-mortem entry rather than a
live outage. Return it as `state: "resolved"` so the dash0 publish dialog can
say "published as resolved". No new error code; publishing after the fact is a
legitimate thing to do, it just must not reopen the page.

Open question for the implementer: should `Severity` still be accepted on a
retroactive publish? Yes — it still labels the past entry on the page.

### 2. `if_untouched` must actually apply to hand-published, incident-linked entries

- Stop stamping `HumanTouchedAt` in `PublishIncident()`. Linking an incident
  to a page is not "taking over the narrative"; editing the title/state/severity
  or appending an update is, and those paths already stamp it (`service.go:420`,
  `:466`, `:513`, `:566`). Keep the stamp in the free-form `Create()` path
  (no incident to resolve against) — or rather, make it irrelevant there, since
  step 2b excludes those entries anyway.
- In `OnIncidentResolved()`, replace `if !pub.AutoCreated` with
  `if pub.IncidentUID == nil` — a publication that is *linked* to the resolving
  incident is in scope of the page's `autoResolve` policy whether a machine or a
  person created it. Free-form publications (no `incidentUid`) stay untouched,
  which preserves the "hand-authored publications are never touched" promise
  for the case it was written for.
- Update the doc comment on `OnIncidentResolved()` and the `HumanTouchedAt`
  comment in `models/incident_publication.go:144` to match.

With 1 + 2, the reported scenario becomes: publish at 12:17:51 → publication is
born resolved; and the near-miss scenario (publish at 12:17:00, incident
resolves at 12:17:22) → `if_untouched`, clean → auto-resolved with the
templated resolved update.

### 3. Make a check-less open publication visible in dash0

Add an amber alert to the top of the dash0 checks list
([`web/dash0/src/routes/orgs/$org/checks.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.index.tsx))
and to the status page detail page: *"N status-page incident(s) are open on
&lt;page&gt; while every linked check is up"*, linking to the publication's edit
route (`status-pages.$statusPageUid.incidents.$uid`). Backend: the existing
org-side publication list with `ActiveOnly` plus a join on the linked
incident's state is enough — expose it as a small
`GET /api/v1/orgs/:org/status-pages/:uid/incidents?active=true&stale=true`
filter (or a boolean `stale` on each item, meaning "linked incident is
resolved"). Use the design-reference alert primitive; do not invent a new one.

### 4. Make the TV headline honest about its cause

When `resolveTvState()` lands on a state that is strictly worse than
`normalizeRollup(overallStatus)`, render a one-line subtitle under the headline:
*"Driven by 1 open incident — all monitored services are passing"* (i18n key in
all locales). The check-driven case keeps today's rendering.

### 5. Ops remedy for the reported page (not code)

Resolve the open publication on the `acme` page by hand once this lands (or
before — it is a one-click action on the status-page incident edit route). The
spec fixes the mechanism; it does not retro-close existing rows.

### Tests

- `incidentpublications`: publishing a resolved incident → publication is
  resolved with `ResolvedAt == incident.ResolvedAt`, two updates posted, event
  `StatusPageIncidentResolved` emitted; publishing a live incident is unchanged.
- `OnIncidentResolved` with a hand-published, incident-linked, **untouched**
  publication under `if_untouched` → resolved. Same but touched → monitoring
  note only (existing behaviour). Free-form publication (no `incidentUid`) →
  untouched, whatever the policy. `always` → resolved even if touched.
- Negative control: `never` still leaves everything alone.
- Public status-page endpoint: a page whose only publication is resolved
  reports `activeIncidents: []`.
- status0 unit test on `resolveTvState` subtitle condition; dash0 e2e for the
  stale-publication alert (present when the linked incident is resolved, absent
  when it is live).
- Run `bun run test:unit` in dash0 and status0 for the new locale keys.
