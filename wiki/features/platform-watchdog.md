# Platform watchdog — how the platform reports on itself

Spec: `specs/done/2026/08/2026-08-24-10-internal-anomaly-watchdog.md`.

## Why it exists

Every other alerting path in SolidPing is **check-level**: a check fails, an
incident opens, an escalation policy pages someone. That means the worst
failure mode of a monitoring product — going blind — is the one failure that
produces **zero signal**. A check that stops being executed keeps its last
state forever, so the dashboard shows green rows with quietly aging "last
result" timestamps.

On 2026-08-24 a region rename stranded 526 of 627 enabled checks for ~3 hours:
345 jobs overdue by up to 3 hours, 61 incidents frozen in `active` (≈50 of them
for targets that had already recovered), and nothing anywhere said a word.

The watchdog is the channel that says the word.

## Shape

| Piece | Where |
|---|---|
| Detectors, transitions, digest | `server/internal/watchdog/` |
| Hourly job + delivery | `server/internal/jobs/jobtypes/job_platform_watchdog.go` (+ `_delivery.go`) |
| Job type | `jobdef.JobTypePlatformWatchdog` = `platform_watchdog` |
| Startup provisioning | `StartupJobRun.ensurePlatformWatchdogJob` |
| Configuration | `platform_watchdog` **system parameter** |
| Metrics | `solidping_watchdog_*` in `internal/prommetrics` |

The job is provisioned at startup unconditionally — it reads its own enable
flag, so turning the watchdog on is a parameter edit rather than a restart.

It self-reschedules from a **`defer`**, on every exit path including the error
returns. This diverges from the sibling internal jobs (snooze sweep, stuck-job
reaper, …), which reschedule only on success and lean on the capped retry chain
otherwise — for them a persistent failure means "this sweep pauses until the
next deploy", which is survivable. For the watchdog it is not: a malformed
`platform_watchdog` parameter burning the retries would leave the platform
silently unwatched, which is the exact failure class the feature exists to
report. The retryable error is still returned, so the retry fires and the
failure stays visible; `CreateJob` dedupes on type+config+org+pending, so the
retry's own reschedule cannot stack a duplicate.

## The four detectors

Each runs **independently** (`stale-checks` excepted, see below). One that errors (or panics) is recorded in
`Report.Failed`, logged, counted on
`solidping_watchdog_detector_failures_total`, and never stops the others. A
watchdog that goes quiet because one query broke would reproduce the exact
failure it exists to catch.

### 1. `dark-region`

Calls **`checks.Service.RegionHealth`** — the spec `2026-08-24-09` ghost
detector — and applies a blast-radius bar on top of its rows. This is a call,
not a copy: there is exactly one definition of "dark" in the codebase, and it
is the one the scheduler's own prefix rule produces.

Reported when a region has `jobsOverdue >= darkRegionMinOverdueJobs` (5) **and**
its oldest overdue job is at least `darkRegionMinOverdueMinutes` (10) old.
`liveWorkers == 0 && jobs > 0` marks it genuinely dark and escalates to
`critical` at 50 overdue jobs or 30 minutes. Remediation carried in the digest:
`POST /api/v1/system/regions/migrate` (spec `2026-08-24-08`).

Private regions (`@<slug>`) are org-relative. `RegionHealth` reports one row per
(organization, slug), served by that org's agents (`agents.last_seen_at`, exact
region match, revoked agents never live), so a connected private location is
never dark. The anomaly subject is `<org>/@<slug>` (e.g. `dark-region:acme/@paris`),
so two orgs' `@paris` never share a fingerprint, and a dark private region's
remediation points at `GET /api/v1/orgs/<org>/agents` instead of a migration
(spec `2026-09-25-01`).

Cloud regions are ALSO watched every minute by the region sweep (below), which
shares this detector's anti-flood marker, so an outage reaches the operator
once, whichever of the two saw it first.

## The per-minute region sweep (`region_health_sweep`)

Spec `2026-09-25-03`. The hourly detector above was the only thing that could
notice the 2026-09-24 `lauterbourg` outage (1 worker lost, 12 checks stopped for
8 hours) and it could not: it was off, it is operator-only, and its 5-job bar
ignores a region with one pinned check, which is exactly the check whose owner
is blind. Code: `internal/regionsweep`, state in `internal/regionoutage`, job in
`jobtypes/job_region_health_sweep.go`, seeded at startup.

- **Runs every minute on the jobs node, whatever `platform_watchdog.enabled`
  says.** It calls `checks.Service.RegionHealthWithJobs` (same scans as
  `RegionHealth`, plus the per-job `check_uid`/`period`) and nothing else.
- **Cloud regions only.** Private `@` rows are filtered out explicitly.
- **States**, per cloud region, in a global `state_entries` marker
  `region-outage:<slug>` (30-day TTL): *dark* = `jobs > 0 && liveWorkers == 0`
  (no job-count floor; the 5-minute liveness window absorbs short blips, so a
  region reads dark 5 to 6 minutes after its last beat); *stalled* = live
  workers but a job overdue past `max(2 × period, 5 min)` (operator only);
  *recovered* after **two consecutive** healthy sweeps. A region whose jobs were
  all migrated away counts as healthy (nothing is stranded) and recovers as
  "moved".
- **Operator**: dark, stalled and recovered go to `platform_watchdog.recipients`
  through `opsnotify.DeliverToUser` (event label `watchdog.region`), filtered by
  `minSeverity` (dark is critical, stalled warning). With the watchdog disabled
  there is nobody to page; the sweep still transitions, logs and meters.
- **No double page**: both sides share `watchdog:anomaly:dark-region:<slug>`.
  The sweep writes it (`watchdog.ClaimNotification`) only when it actually
  delivers, and stays quiet if the digest already holds it; the digest then
  reads the outage as ongoing. The only thing the sweep re-announces over an
  existing marker is its own stalled → dark escalation. The watchdog never
  resolves a `dark-region:<slug>` fingerprint while a `region-outage:<slug>`
  marker exists (its higher bar makes "not seen" meaningless there); the sweep
  clears the shared marker on recovery and announces it if it existed.
- **Orgs**: when a region turns dark, every non-internal enabled check with a
  job there is classified *blind* (every job region out of service: a dark
  cloud region, or a private region with no live agent) or *reduced*. Each org
  with at least one blind check gets one `region.offline` event and one email
  per owner/admin (`region-offline.html`, its own blind checks listed with
  links, capped at 20, plus the reduced count). The org UIDs go in the marker;
  an org that becomes blind later in the same outage is told then, an org
  already told never is again. Recovery sends `region.recovered` +
  `region-recovered.html` to exactly those orgs. Chat integrations are out of
  v1 (their senders are incident-shaped).
- **dash0**: `GET /orgs/:org/regions` carries `status` (`online|offline`) and
  `offlineSince` on cloud regions (offline = marker phase dark; stalled stays
  online). The check detail page shows a destructive banner for a blind check
  and a warning for a reduced one, the checks list names the checks that
  stopped, and the check form marks offline regions.

### 2. `fleet-collapse`

Results produced in the **last completed hour** vs. the **same hour a day
earlier** (never the previous hour — check traffic has a daily shape and a quiet
night would page every night). One conditional-aggregation query. Anomaly when
the baseline is at least `fleetMinBaseline` (100) and the drop exceeds
`fleetDropPercent` (50); critical past `fleetCriticalDropPercent` (80).

This is the catch-all for stranding causes nobody has imagined yet.

### 3. `stale-incidents`

Active incidents whose check has produced no result for longer than
`max(staleIncidentPeriodMultiplier × period, staleIncidentMinMinutes)` —
default `max(3 × period, 15m)`. Disabled and deleted checks are excluded: a
check nobody executes on purpose is not a mystery. Folded into **one** anomaly
carrying the count and the three oldest, never one page per frozen incident.

### 4. `stale-checks`

Checks in the `stale` ("No data") status — no real result for
`max(3 × period, 5 min)`, set by the minute freshness sweep (spec
`2026-09-25-02`) — whose placement regions are **not all dark**. A stale check
in a dark region is the dark-region detector's to report (and the org's region
notice's), so it is left out: one outage, one report. What remains is the
platform bug class — a stuck scheduler, a lease leak, rate limiting — which only
the operator can fix. An any-region job counts as dark only when every cloud
region is. Folded into **one** anomaly (subject `unexplained`) carrying the count
and the three longest-silent checks; critical at `staleChecksCriticalCount`.

This detector reads the dark-region detector's `RegionHealth` report, so it is
the one detector that is **not** independent: when the dark-region pass fails,
`stale-checks` fails too (with `ErrRegionHealthUnavailable`) rather than guess —
reporting every stale check would double-report the outage, reporting none
would read as healthy.

## Anti-flood: transitions, not state

A fingerprint (`<detector>:<subject>`, e.g. `dark-region:eu2`) is persisted as a
**global** `state_entries` row keyed `watchdog:anomaly:<fingerprint>`
(`organization_uid IS NULL`).

| Transition | Behaviour |
|---|---|
| **new** | notify |
| **ongoing** | silent until `renotifyAfterMinutes` (24h), then re-notify as "STILL BROKEN since …" |
| escalated severity | re-notify immediately — "5 jobs late" and "the region went dark" are not the same page |
| **resolved** | notify recovery **once**, then clear the marker |

Load-bearing rule: a fingerprint is only resolved when **its detector actually
succeeded** this run. A query error must never be laundered into a false
"recovered" — telling an operator the outage is over while it is still going is
worse than saying nothing.

`minSeverity` filters the anti-flood ledger *and* delivery together: a marker
written for an anomaly the digest never mentions would record a notification
that never happened, and would then suppress the real one when the anomaly
escalates past the bar. Logs and metrics always see everything.

## Configuration

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"value":{"enabled":true,"recipients":["<user-uid>"],"minSeverity":"warning"}}' \
  "$BASE/api/v1/system/parameters/platform_watchdog"
```

Super-admin only (the same `/system/parameters` CRUD everything else uses), and
validated on write (`watchdog.ValidateParameter`) so a value the hourly job
could not decode is rejected while the operator is still looking at it.

Every threshold is overridable; unset (or zero) means the default:

| Key | Default |
|---|---|
| `enabled` | `false` |
| `recipients` | `[]` (user UIDs) |
| `minSeverity` | `warning` |
| `intervalMinutes` | 60 |
| `renotifyAfterMinutes` | 1440 |
| `darkRegionMinOverdueJobs` / `darkRegionMinOverdueMinutes` | 5 / 10 |
| `darkRegionCriticalJobs` / `darkRegionCriticalMinutes` | 50 / 30 |
| `fleetDropPercent` / `fleetMinBaseline` / `fleetCriticalDropPercent` | 50 / 100 / 80 |
| `staleIncidentMinMinutes` / `staleIncidentPeriodMultiplier` | 15 / 3 |
| `staleIncidentCriticalCount` / `staleIncidentScanLimit` | 10 / 2000 |
| `staleChecksCriticalCount` | 10 |

## Delivery

One **digest per run**, never one message per anomaly, delivered through each
recipient's own enabled `user_notification_routes` in `position` order —
exactly what incident paging does. No new medium, no hardcoded webhook.

Routes are collected across every organization the recipient belongs to
(routes are org-scoped because a Slack DM needs the org's bot token, while the
watchdog reports on the platform and has no org of its own), de-duplicated by
contact **type + normalized value** — `user_contacts` rows are org-scoped, so
the same address in two orgs is two rows and one human, and a UID-keyed dedup
would mail them twice.

Supported: **email** (enqueued as a normal `email` job), **Telegram**,
**Slack DM**, **Web Push**, **SMS**. WhatsApp is template-gated by Meta and
cannot carry free-form text, so such a route is skipped with an explicit WARN.

Nothing is ever dropped silently:

- recipient with no deliverable route → `WARN … digest undeliverable` naming the user;
- `enabled: true` with no recipients → the run still evaluates, logs and meters,
  and WARNs that nobody is configured;
- `enabled: false` → no queries, no state, no metrics, no delivery; only the
  reschedule.

## Metrics (the out-of-band half)

The digest is delivered by the very process it monitors, which is better than
nothing but must never be the only signal. These gauges let an external
Prometheus alert independently:

| Metric | Meaning |
|---|---|
| `solidping_watchdog_anomalies{detector,severity}` | anomalies found by the last run (explicit `0` when healthy, so alert rules have a series to evaluate) |
| `solidping_watchdog_stranded_jobs` | overdue jobs across every flagged region |
| `solidping_watchdog_stale_incidents` | frozen active incidents |
| `solidping_watchdog_detector_failures_total{detector}` | detector runs that errored |
| `solidping_watchdog_last_run_timestamp_seconds` | staleness of the watchdog itself |
| `solidping_workers_active{region}` | live workers per cloud region, from `RegionHealth`. Written every minute by the region sweep (its only writer), even while the watchdog is disabled. Spec 2026-09-25-03 proposed a `solidping_region_live_workers` gauge: it would have meant exactly this, so it was not added under a second name |
| `solidping_region_dark{region}` | `1` while the region sweep holds a cloud region as dark, `0` otherwise; every cloud region gets a series |
| `solidping_checks_stale{region}` | checks currently `stale`, by placement region (`any` for an any-region job, `private` for every `@` region). Published every minute by the freshness sweep, not by the watchdog, so it exists even while the watchdog is disabled |

A detector that errored leaves its anomaly gauge at the previous value rather
than writing a `0`: publishing "healthy" as a fact when the watchdog could not
look is the same lie the feature exists to prevent.

## Out of scope (v1)

A dashboard surface for watchdog state, per-org self-monitoring for customers
(this is the *server operator's* tool), and any new delivery medium.
