---
model: opus
effort: high
---

# A dark region is only reported by an hourly operator digest, and the orgs whose checks stopped are never told

## Problem

On 2026-09-24 the only worker of the `lauterbourg` region (node eu3) lost its
network around 12:28 UTC. From 13:41 no result came out of that region for about
8 hours:

- 26 `check_jobs` sat unclaimed. 12 enabled checks were pinned to `lauterbourg`
  alone (11 in the showcase org `public`, 1 in another org), so they stopped running
  entirely.
- Nobody was told. The operator found out by opening a check page by chance.
  The affected orgs were never told at all.

What exists today, and why it did not help:

- **The platform watchdog** (spec `2026-08-24-10`,
  [job_platform_watchdog.go](server/internal/jobs/jobtypes/job_platform_watchdog.go))
  has a `dark-region` detector
  ([detect_regions.go:62-92](server/internal/watchdog/detect_regions.go#L62)).
  It had several limits:
  - It was **off** in prod: the `platform_watchdog` parameter had never been
    written. It was enabled on 2026-09-24 with `intervalMinutes: 15`.
  - It is operator-only by design: org-facing monitoring was explicitly left
    out of scope
    ([spec, lines 128-131](specs/done/2026/08/2026-08-24-10-internal-anomaly-watchdog.md)).
  - It cannot simply run every minute. `Evaluate` always runs all three
    detectors ([watchdog.go:221-251](server/internal/watchdog/watchdog.go#L221)),
    and fleet-collapse scans raw `results` over two one-hour windows with no
    fitting index
    ([detect_fleet.go:46-55](server/internal/watchdog/detect_fleet.go#L46)).
  - Its default bar (at least 5 overdue jobs, oldest 10+ minutes late) ignores
    a region with fewer than 5 pinned checks. Those are exactly the checks whose
    owners are completely blind.
- **Orgs have no channel for platform events.**
  - Incident delivery needs an incident and a check. The sender payload carries
    both ([sender.go:16-81](server/internal/notifications/sender.go#L16)).
    `incident_notifications.incident_uid` is `NOT NULL`. The webhook sender
    dereferences both without a nil check
    ([webhook.go:405-435](server/internal/notifications/webhook.go#L405)).
  - The only per-org, non-incident notice is the custom-domain demotion
    ([customdomain/alert.go:66-179](server/internal/customdomain/alert.go#L66)):
    an `events` row plus one `email` job per owner or admin.
- **Region liveness is not visible to users.**
  - `GET /api/v1/regions` shows capabilities as `unknown` when no worker is live
    ([capabilities.go:38-54](server/internal/regions/capabilities.go#L38)). That
    is the only hint.
  - `/system/regions/health` is super-admin only
    ([server.go:1648-1652](server/internal/app/server.go#L1648)), and no dash0
    page uses it.

`RegionHealth`
([region_health.go:89-150](server/internal/handlers/checks/region_health.go#L89))
is cheap enough to run every minute at the current size. It does three full
scans (`checks.regions`, `check_jobs(region, scheduled_at)`, `workers`) and sums
them in Go. It is also the single definition of "dark" that the watchdog already
reuses.

## Proposal

### 1. A per-minute `region_health_sweep` job on the jobs node

- Build it on `periodicSweep`
  ([self_rescheduling_sweep.go:56-114](server/internal/jobs/jobtypes/self_rescheduling_sweep.go#L56)).
- Ensure it at startup like `slo_burn_eval` and `degraded_eval`
  ([job_startup.go:156-180](server/internal/jobs/jobtypes/job_startup.go#L156)).
- It runs on the jobs node, which is not tied to any region. Each run calls
  `RegionHealth` and nothing else. It does not run the other watchdog detectors.

Per **cloud region**, a two-state machine:

- **dark** when the region has at least one job and `liveWorkers == 0`. The
  5-minute `WorkerLivenessWindow`
  ([capabilities.go:50-54](server/internal/regions/capabilities.go#L50)) already
  absorbs short blips, so a region is reported dark about 5 to 6 minutes after
  its last worker beat. There is no job-count floor: one pinned check is enough.
- **healthy again** when `liveWorkers > 0` on two consecutive sweeps and no job
  is overdue by more than `max(2 × period, 5 min)`.
- A region with live workers but jobs overdue past that bar is **stalled**
  (workers alive but not claiming). Report it to the operator only, since the
  cause is ambiguous.

Private regions (`@<slug>`, scoped by `organization_uid`) are out of scope here.
The org owns that agent, and
spec `2026-09-25-05` gives it a real liveness monitor. This spec depends on
`2026-09-25-01` (private regions counted correctly). Until that lands, filter
`@` regions out explicitly so they can never be reported dark by mistake.

### 2. Operator: paged at the transition, not at the next digest

- On dark, stalled and recovered transitions, deliver to the existing
  `platform_watchdog.recipients` through `opsnotify.DeliverToUser`, the same
  routes as the watchdog digest.
- The per-minute sweep and the hourly watchdog `dark-region` detector must never
  both notify the same outage. Share the anomaly marker
  (`watchdog:anomaly:dark-region:<slug>`, `transitions.go`) so that whichever
  sees it first records it and the other reads it as ongoing. Alternatively,
  drop `dark-region` from the digest once the sweep exists; the implementer
  picks, a test pins it.
- The sweep must work even when `platform_watchdog.enabled` is false:
  - with no recipients, it still logs and records state;
  - it always publishes its metrics (see 5).

### 3. Orgs: one notice per outage, one per recovery

When a region turns dark, compute the affected orgs in the same pass. Extend the
`check_jobs` scan in `RegionHealth` with `organization_uid` and `check_uid`, and
leave out internal checks. Classify each affected check:

- **blind**: every region of the check is dark. The check is not running at all.
- **reduced**: some of its regions are still live.

Every org with at least one **blind** check gets exactly one notice:

> Region **lauterbourg** has been offline since 13:41 UTC. 11 of your checks run
> only from this region and are not being checked: … (list, capped, with links).
> 3 other checks keep running from their other regions.

- **Since:** `LastWorkerSeenAt` from `RegionHealth`, not the time the sweep
  noticed.
- **Recipients:** org owners and admins by email, following the custom-domain
  pattern ([alert.go:66-179](server/internal/customdomain/alert.go#L66)). Also
  write an org-scoped `events` row with the new event types `region.offline` and
  `region.recovered`.
  - Adding an event type means updating [event.go](server/internal/db/models/event.go)
    and the two exhaustive switches in
    [incidents/service.go:1446-1560](server/internal/handlers/incidents/service.go#L1446)
    and [system/service.go:182-226](server/internal/handlers/system/service.go#L182).
- **Recovery:** one notice to exactly the orgs that got the offline notice, with
  the duration and "checks resumed". Keep the list of notified org UIDs in the
  region's marker (`state_entries`, global, 30-day TTL like the watchdog). The
  marker has no org because `ListStateEntries` cannot list one prefix across
  orgs ([postgres.go:4379-4383](server/internal/db/postgres/postgres.go#L4379)).
- **No re-notification** of orgs while the outage lasts.
- Orgs with only **reduced** checks get no push notice. They see the dash0
  banner (4).

Chat integrations (Slack, Discord, webhook…) stay out of v1. Their senders are
incident-shaped, and a generic "notice" payload is its own change. See open
questions.

### 4. dash0: say it where the user looks

- On the checks list and check detail, any check with a job in a dark region
  shows a banner:
  - blind: "Region lauterbourg offline since 13:41: this check is not running";
  - reduced: "…: running from 2 of 3 regions".
- Expose the state through the org regions endpoint (`/orgs/:org/regions`
  gains `status: online | offline` and `offlineSince`), so the check form can
  also warn before someone pins a check to an offline region.
- Follow the design reference and add locale keys for every new string in every
  locale.

### 5. Metrics, always on

- `solidping_region_dark{region}` (0/1) and
  `solidping_region_live_workers{region}`, set by the sweep on every run,
  whatever the watchdog config says.
- Spec `2026-09-25-01` wires or removes the dead `solidping_workers_active`.
  Don't publish two metrics with the same meaning.

### Tests

- A region with 1 pinned check goes dark: the org is notified once, the operator
  once, and there is no second notice on the next sweeps.
- A multi-region check with one dark region counts as reduced, not blind, and
  its org gets no push notice.
- A worker that misses beats for under 5 minutes causes no transition.
- Recovery notifies exactly the orgs recorded in the marker, once.
- Two orgs, one region: two notices, each listing only its own checks.
- A private `@<slug>` region is never reported by this sweep.
- The hourly watchdog and the sweep do not double-notify the operator.
- `platform_watchdog.enabled = false`: the sweep still transitions and meters.

## Open questions

- Chat integrations: extend the sender payload with a non-incident "notice"
  (Slack/Discord/Teams/webhook), or give orgs a "platform notices" routing
  setting? Email to admins is the v1 floor.
- Should a public "platform status" (regions online/offline) be exposed on
  `status.solidping.io`? That is a separate SolidPing instance today, so it is
  a config change, not code.
- Cost: `RegionHealth` scans `checks`, `check_jobs` and `workers` fully. Fine at
  a few hundred checks. Measure at 10k jobs, and move to a SQL `GROUP BY` if a
  sweep takes over ~200 ms.

## Resolved open questions

- **Chat integrations for non-incident notices: extend the sender payload now, or add a
  "platform notices" routing setting?** Decision: ship v1 with email-to-admins only, as the
  spec already scoped it. Defer the chat-integration extension mechanism to a follow-up spec
  once there's real demand for it — don't build speculative routing now. Trade-off: orgs that
  only use Slack/Discord/Teams get no push notice for a region outage until that follow-up
  lands; email remains their only channel in the meantime.
- **Public "platform status" page on `status.solidping.io`?** Decision: out of scope for this
  spec. It is a config change on a separate SolidPing status-page instance, not code in this
  repo — no engineering action here. Whoever owns that instance can opt in later.
