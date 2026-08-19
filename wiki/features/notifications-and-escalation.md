# Notifications & escalation

How a check failure becomes a Slack message, an email, and (if nobody
acks) a 3 AM page to whoever's on call this week.

This page is the end-to-end view a contributor needs before touching the
incident or notifier code: it walks from the moment a worker writes a
result through the state machine, the incident lifecycle, the events
table, and out to the channels that actually buzz. For per-resource API
details see [api-specification/notifications.md](../api-specification/notifications.md). For the
data model see [database-model/notifications.md](../database-model/notifications.md)
and [database-model/results-incidents.md](../database-model/results-incidents.md). For the
dependency-graph rules behind cascade rollup see
[check-dependencies.md](check-dependencies.md).

The reference implementation lives in two files:

- `server/internal/checkworker/worker.go` — execution & result persistence
- `server/internal/handlers/incidents/service.go` — state machine,
  incident lifecycle, event emission, notification queueing

## The pipeline

```
                        ┌─────────────────────┐
                        │  worker runs probe  │  worker.go:420 executeJob
                        │  → models.Result    │  worker.go:599 saveResult
                        └──────────┬──────────┘
                                   ▼
                        ┌─────────────────────┐
                        │  ProcessCheckResult │  service.go:80
                        │  • maintenance gate │  service.go:86
                        │  • classify result  │  service.go:101
                        │  • derive status &  │  service.go:120, 124
                        │    incident clocks  │
                        │  • route by         │  service.go:304
                        │    (failure?,group?)│
                        └──────────┬──────────┘
                                   ▼
                        ┌─────────────────────┐
                        │  open / escalate /  │  service.go:329 handleFailure
                        │  resolve / reopen   │  service.go:369 handleSuccess
                        │  → emitEvent(...)   │  service.go:1034
                        └──────────┬──────────┘
                                   │  PagingSuppressed && !Resolved? → no fan-out
                                   ▼
                       events table + dispatch
                                   │
                  ┌────────────────┴────────────────┐
                  ▼                                 ▼
         queueNotifications                queueGroupNotifications
         (single-check incident)           (group incident: one
         service.go:1176                    message per (channel, type))
                  │                                 │
                  └────────── notification job ─────┘
                                   │
                                   ▼  jobs/jobtypes/job_notification.go:79
                            sender.Send(...)
                            (slack / discord / email
                             / webhook / ntfy / …)
```

Two parallel branches feed off `EventTypeIncidentCreated`:

1. **Notifications** — fan out *now* to every channel bound to the check
   (or, for group incidents, to the union of every currently-failing
   member's channels). One job per `(channel, eventType)` pair so each
   Slack channel only buzzes once even if the underlying group spans ten
   checks.
2. **Escalation cycle** — if the check has an escalation policy attached,
   `scheduleEscalationPolicy` (`incidents/service.go:1222`) creates one
   delayed job per step. Steps fire on the policy's cumulative
   `delayMinutes` schedule until the incident is acked, snoozed, resolved,
   or the policy's `repeatMax` is reached.

Resolved and reopened events fan out to channels but **do not** start a
new escalation cycle: resolved is final; reopened is a relapse and the
original cycle's pending steps were either already fired or canceled by
the ack/snooze that came before.

## From check result to incident — the state machine

Once a result is persisted, `ProcessCheckResult`
(`incidents/service.go:80`) is the sole entry point into the rest of the
pipeline. Three early gates run first:

1. **Maintenance window** (`service.go:86`). If the check is inside an
   active window, return immediately — no state mutation, no incident,
   no event. The result is still persisted; it just doesn't trigger
   paging.
2. **Classify** (`service.go:101`). `Up` → success. `Down | Timeout |
   Error` → failure (the state machine doesn't care *why* the probe
   failed, only that it did). `Created | Running` → return early without
   touching state.
3. **Load active incident** (`service.go:114`). Needed by the next step
   to distinguish `down` (incident open or threshold crossed) from
   `validating` (failures observed, threshold not crossed yet).

Then two pure functions compute the new state — pure so they can be
tested in isolation and so `ProcessCheckResult` stays under the
cyclomatic-complexity cap.

### `deriveCheckStatus` — `service.go:222`

Returns `(status, streak, statusChangedAt)`:

- `status` ∈ {`up`, `validating`, `down`}.
- `streak` — how many consecutive results have produced the current
  status. Crucially, `validating` and `down` are sub-states of the same
  failing lifecycle: the streak keeps growing across the
  validating→down boundary, and `statusChangedAt` is **not** bumped on
  that transition. Only the up↔failing edge bumps it.
- `validating` means "we've seen a failure but haven't crossed the
  confirmation threshold yet — no incident is open." `down` means "an
  incident is open (or just opened) for this failure."

The status pick on a failing result (`pickStatus`, `service.go:263`):

```
if isSuccess                                        → up
if activeIncident != nil                            → down
if isFailure && confirmation period has elapsed     → down
otherwise                                           → validating
```

`confirmationElapsedDerive` (`service.go:283`) handles the very-first
failure where `FirstFailureAt` isn't persisted yet by treating
"streak == 1" as "the failure starts right now" — which makes
`ConfirmationPeriodSeconds == 0` ("open immediately") work correctly.

### `deriveIncidentClocks` — `service.go:163`

Manages two timestamp columns on `checks`:

| Column | Set when | Cleared when |
|---|---|---|
| `first_failure_at` | first failure while no incident is open | first success while no incident is open (flap reset) |
| `first_success_since_failure_at` | first success while an incident is open | failure during the recovery window (recovery clock resets) |

These two clocks alone implement the **confirmation** (failure must
persist long enough to open) and **recovery** (success must persist
long enough to resolve) windows. The tri-state pointer / `Clear*` flag
pattern lets `db.UpdateCheckIncidentClocks` write SET / SET NULL /
leave-alone without ambiguity.

After `UpdateCheckStatus` and `UpdateCheckIncidentClocks` write the new
state (`service.go:127-136`), `applyClocks` (`service.go:199`) mirrors
the same change into the in-memory `*Check` so the downstream handlers
read the just-written values without a re-fetch.

## Incident lifecycle — open, escalate, resolve, reopen

`routeCheckResultWithIncident` (`service.go:304`) splits four ways on
`(isFailure, isGroup)`. Group incidents (one incident covering multiple
checks in a check group) take parallel paths with shared helpers; the
group-specific bits live in `handleGroupFailure` (`service.go:651`) and
`updateGroupMemberOnFailure` (`service.go:667`). The rest of this
section follows the per-check paths.

### Failure path — `handleFailure` (`service.go:329`)

```
incident == nil ?
├─ no  → bump incident.FailureCount;
│        if FailureCount ≥ check.EscalationThreshold and not yet escalated,
│        set incident.EscalatedAt = now and emit IncidentEscalated.
└─ yes → confirmation elapsed ?
         ├─ no  → return; stay validating.
         └─ yes → createOrReopenIncident.
```

Escalation is a one-shot per incident: `EscalatedAt` is checked first,
so an incident only escalates once even if it accumulates hundreds of
failures.

### Reopen vs create — `createOrReopenIncident` (`service.go:476`)

Tries `tryReopenIncident` first to avoid spamming new incident rows on
flapping checks:

- **Reopen window** = `calculateCooldown(check)` =
  `check.Period * ReopenCooldownMultiplier` (default 5), clamped to
  [2 min, 30 min] (`service.go:490-517`). A `0` multiplier disables
  reopening entirely.
- A recently resolved incident is reopened only if it was **not**
  manually acked (`AcknowledgedBy != nil` blocks reopen,
  `service.go:550`) and the check definition hasn't changed since
  resolution (`UpdatedAt > ResolvedAt` blocks reopen, `service.go:555`).
  The intent: an operator who acked the closure and went home shouldn't
  see the same incident silently revive, and a check whose config
  changed is a different question entirely.

`reopenIncident` (`service.go:563`) flips state back to `active`,
clears `ResolvedAt` and the ack fields, bumps both `RelapseCount` and
`FailureCount`, re-applies rollup (the parent topology may have changed
during the cooldown), and emits `EventTypeIncidentReopened`.

If reopen doesn't apply, `createIncident` (`service.go:410`) runs:

1. `models.NewIncident(orgUID, checkUID, result.PeriodStart, title)` —
   title is `"<slug> is down"` (`generateIncidentTitle`,
   `service.go:1020`).
2. `applyRollup(ctx, check, incident)` — see [Cascade rollup](#cascade-rollup).
3. `db.CreateIncident` writes the row.
4. `emitEvent(EventTypeIncidentCreated, ...)` fires (`service.go:424`).

### Success path — `handleSuccess` (`service.go:369`)

```
incident == nil                       → nothing to do.
recovery window not yet elapsed       → leave incident active.
recovery window elapsed               → resolveIncident.
```

`resolveIncident` (`service.go:437`) sets `state='resolved'` and
`resolved_at = result.PeriodStart`, emits `EventTypeIncidentResolved`,
and re-evaluates suppressed children — see [Cascade rollup](#cascade-rollup).

## Cascade rollup

If the failing check has a hard parent dependency that is itself
failing, the new incident is created with `PagingSuppressed = true`.
The event still gets recorded (operators want to see the rolled-up
child in the timeline), but `emitEvent` skips the channel fan-out for
everything *except* the resolved event (`service.go:1059`).

`applyRollup` (`rollup.go:26`) walks the check dependency graph BFS,
restricted to fully-hard paths, up to **10 hops** (`rollupDepthCap`).
For every reached ancestor it queries open non-suppressed incidents
inside the **correlation window** (≥ 5 min,
`rollupMinWindow`). The deepest such incident wins, and the child gets
`CausedByIncidentUID = root.UID` and `PagingSuppressed = true`.

When the parent recovers, `resolveIncident` calls
`reEvaluateRollupChildren` (`rollup.go:151`):

- For each suppressed child, check whether its own check is still
  `down`. **If yes**, clear `paging_suppressed` and emit
  `EventTypeIncidentReopened` *now* — the child finally pages because
  the upstream story no longer explains it. **If no**, silently detach
  (clear the rollup attribution; no event).

This is the "fix the upstream and the downstream incidents page if
they're still broken" behaviour. The graph rules and edge cases are in
[check-dependencies.md](check-dependencies.md).

## Which lifecycle events notify?

The dispatcher in `emitEvent` (`incidents/service.go:1034`) is explicit
about who notifies. Every lifecycle change writes an `Event` row
(see [Event payload](#event-payload) below); only some of them queue
notification jobs.

| Event | Trigger | Channels notified | Escalation cycle |
|---|---|---|---|
| `incident.created` | `createIncident` | yes | starts |
| `incident.escalated` | `FailureCount ≥ EscalationThreshold` (first time) | yes | (in flight) |
| `incident.resolved` | `resolveIncident` (recovery elapsed, or manual) | yes | canceled |
| `incident.reopened` | `reopenIncident` within cooldown, **or** `reEvaluateChild` un-suppresses | yes | does not restart |
| `incident.acknowledged` | `AcknowledgeIncident` (web/Slack/email magic link) | no | canceled |
| `incident.unacknowledged` | manual ack clear | no | does not restart |
| `incident.snoozed` | `SnoozeIncident` | no | canceled until snooze expires |
| `incident.unsnoozed` | snooze cleared, or `SweepUnsnooze` (`service.go:2151`) when window closes | no | does not restart |
| `incident.comment` | `addCommentByOrgUID` (dashboard, API, Slack `/comment`, Telegram `/comment`, Slack thread reply in `all` mode) | yes — see below | — |
| `incident.escalation_failed` | a step couldn't deliver (no on-call user, empty schedule) | no | — |
| `check.created/updated/deleted` | check CRUD endpoints | no | — |
| `org.activation.*` (5 milestones) | activation funnel (`internal/activation/`) | no | — |

Full enum: `db/models/event.go:12-56`.

The "no notifications on ack/snooze" decision is intentional: at 3 AM
you already know you acked your own page; channel members don't need an
"acked!" buzz. If you need that signal, watch the events stream.

### Comment fan-out (spec `2026-08-15-08`)

`incident.comment` is the one notifying event that does NOT travel through
`emitEvent`. `addCommentByOrgUID` writes its own event row and calls
`queueCommentNotifications` directly, because the fan-out needs two things
`emitEvent` never carries: the comment body, and the connection the comment came
from. The `emitEvent` switch keeps an explicit (empty) `incident.comment` case
so it cannot be re-read as "comments are silent" — which is the bug this
replaced.

The connection set is identical to the lifecycle path (per-check
`ListChannelsForCheck`, or the union of a group incident's member checks). Two
filters apply on top:

1. **Registry opt-out** — `notifications.AcceptsEventType(connType, eventType)`
   (`notifications/registry.go`). The per-sender table declares
   `NotifiesComments`; only `twilio` sets it false, because an SMS or a phone
   call per operator note is noise the recipient pays for. Everything absent
   from the table, including email, gets the default (all on). Call sites never
   name an integration type, which is what keeps the opt-out in one place.
2. **Echo suppression** — `isCommentEchoOrigin`. A comment carries
   `EchoOriginTeamID`, set ONLY by the Slack thread-reply ingest path, and every
   Slack connection in that workspace is skipped. Matching is at workspace
   level, not per connection row, because the incident's Slack thread is stored
   once per incident: any connection in that workspace would post into the very
   thread the author is reading. `/comment` deliberately leaves
   `EchoOriginTeamID` empty — a slash command posts nothing visible, so the
   channel that typed it must still receive the fan-out.

   This is distinct from the `bot_id` ingest guard
   (`slack/events.go`), which stops our own posts being re-ingested. Both are
   needed: one prevents an echo, the other prevents a loop.

The comment body travels in `NotificationJobConfig.Comment`
(`notifications.CommentInfo{Text, AuthorName, Source}`) rather than being
re-read from the event row at send time, so a job renders exactly the text that
was commented. Audit rows are written exactly as for lifecycle events.

**v1 limitation:** comment fan-out reaches **check-attached channels only**.
The `queueResolutionNotice` person-contact path (Telegram contacts paged by an
escalation policy) is out of scope — someone paged individually is not
forwarded comments unless a channel is attached to the check.

### Closing the loop with person contacts

The table above is about **channels**. Person contacts (Telegram today;
WhatsApp/SMS are the planned follow-ups) are never reached by
`queueNotifications` — only the escalation step pages them, and it stops firing
the moment the incident is acked or resolved. Left there, the on-call engineer
gets a red Telegram alert and is never told it ended.

`incident.resolved` therefore also queues one **`incident_resolution_notice`**
job (`jobtypes/job_incident_resolution_notice.go`), enqueued after the
`PagingSuppressed` early return and before the group branch, so rolled-up
children stay silent and group incidents are covered. The job:

- fans out over the **thread anchors** (`telegram_msg:<incidentUID>:<chatID>`
  state entries) — the exact set of chats that received a page — sending the
  RESOLVED notice through the same send path as an alert, so it threads under
  the original message, rewrites that message with `BuildResolvedOriginalHTML`
  and an explicitly empty keyboard (which is what removes the stale Acknowledge
  button), and honors Telegram's `retry_after`;
- falls back to the `incident_notifications` audit trail (channel `telegram`,
  status `sent`, event `incident.escalated`) for chats whose anchor expired past
  its 7-day TTL, resolving each paged user's *current* verified contact;
- **claims** each chat by deleting its anchor before sending (`DeleteStateEntry`
  is a compare-and-set on both drivers, so two concurrently running notice jobs
  cannot both notify the same chat) and puts the anchor back whenever the notice
  does not go out;
- writes a `telegram_resolved:<incidentUID>:<chatID>` marker after each
  delivery, which together with the consumed anchor makes retries and a
  reopen → re-resolve cycle send **exactly one** notice — a relapse never
  re-pages person contacts, so announcing its end would be noise;
- reserves the hourly Telegram runaway guard per send (a mass recovery must not
  turn into an unbounded burst) and returns a retryable error only for
  network-class failures.

This job type is the one exemption in `jobsvc.CancelPendingForIncident`: ack /
snooze / resolve cancel every pending **page** for an incident, but never the
all-clear owed to someone who already got one. Without the exemption, pressing
Acknowledge on a just-resolved alert would delete the message that removes that
very button.

### Event payload

`emitEvent` writes an `Event` row with `UID`, `OrganizationUID`,
`IncidentUID` (always set for incident events), `EventType`, `ActorType`
(`system` by default, `user` when the payload carries `actor_uid`),
optional `ActorUID`, and a `Payload` JSONB blob. The payload keys are
centralized as constants at the top of `incidents/service.go`:
`check_uid`, `check_slug`, `started_at`, `resolved_at`,
`duration_seconds`, `failure_count`, `escalation_threshold`,
`relapse_count`, `effective_recovery_threshold`, `total_failures`,
`result_uid`, plus the operator-action keys `via`, `actor_uid`,
`acknowledged_by_email`, `slack_user_id`, `slack_username`, `note`.

## Group incidents

When several checks in the same `check_group` fail at once, the system
opens a single **group incident** instead of one per check. `emitEvent`
detects this via `incident.CheckGroupUID != nil` and dispatches through
`queueGroupNotifications` (`incidents/service.go:1109`).

The dedup rule:

- For `incident.created` / `incident.escalated` / `incident.reopened`:
  fan **one** notification per `(channel, eventType)` where the channel
  set is the *union* of every currently-failing member's bound channels.
  Recovered members do not bring their channels into mid-incident events.
- For `incident.resolved`: include channels from *every* member so that
  every channel that fired at open time hears about the close, even if
  that specific member recovered earlier in the incident's life.

The incident's `title` is rebuilt as members fail and recover —
`"<group> — N/M checks down"` (`formatGroupTitle`, `service.go:623`).

## Suppression: maintenance, ack, snooze, manual resolve

Three independent layers can suppress notifications. They compose: any
one of them firing kills the page-out. Cascade rollup is the fourth and
covered above.

### Maintenance windows

Already noted in the state-machine section: when a check is inside an
active maintenance window, `ProcessCheckResult` returns *before* the
state machine runs (`service.go:86`). There is no incident, no event,
no notification. The check status still updates and results are still
recorded; they just don't trigger paging. When the window ends, the
next failing result will open a fresh incident if the failure persists.
There is no "deferred" incident that fires at window end.

### Per-incident silencing (ack / snooze / manual resolve)

Operators can ack, snooze, or manually resolve an incident at any time.
All three paths call `cancelPendingNotifications`
(`incidents/service.go:1784`) which delegates to
`jobsSvc.CancelPendingForIncident` (`jobs/jobsvc/service.go:60`). That
sweep soft-deletes every pending job whose config JSON has `incidentUid`
matching the incident — **this catches both notification jobs and
escalation step jobs**, because the cancel sweep keys on config and
doesn't care which job type it is. The invariant is pinned by
[`TestCancelPendingForIncidentDropsEscalationSteps`](../../server/internal/handlers/escalationpolicies/runtime_test.go).

Snooze adds a `notBefore` argument so jobs scheduled inside the snooze
window are canceled but jobs scheduled to fire *after* the window are
left alone (they'll fire only if the incident is still open then).

This is why the escalation/notification schedulers don't need an
explicit "watch for ack" hook — they're idempotent fire-and-forget jobs
and the operator action poisons the queue.

## Channels (a.k.a. connections)

The notification target is stored as an `integration_connections` row
in the database and called a "channel" everywhere a user sees it.
The split is a known papercut tracked in spec
[2026-05-07-03-align-channel-and-connection-naming.md](../../specs/done/2026/05/2026-05-07-03-align-channel-and-connection-naming.md).

A channel:

- has a `type` (one of nine: slack, discord, email, webhook, googlechat,
  mattermost, ntfy, pagerduty, pushover) that picks the dispatcher;
- carries type-specific config in a JSONB column with secret fields split
  into a `settings_private` envelope (see the encryption-at-rest section
  of `CLAUDE.md`);
- is bound to checks individually via `check_connections`. A channel
  marked `is_default = true` is auto-bound to every newly created check.
  Existing checks are not touched when the default flag flips.

A channel-bind is the smallest unit of "who hears about which check".
The escalation policy is the larger unit — it can target a channel, a
user, an on-call schedule (resolved at fire time), or all org admins.

The notification job runner (`jobs/jobtypes/job_notification.go:79`)
loads the connection, decrypts `settings_private` if encryption is
enabled, loads the incident and check, and dispatches via the
type-specific sender registered in `notifications/registry.go`. Network
errors are wrapped as retryable so the jobs runner schedules a retry;
other errors fail the job permanently.

## Escalation policies

An **escalation policy** is an ordered list of steps with delays-from-start.
Each step targets one of:

- **user** — direct notification to a specific user via their default
  channel preferences (or all channels they're a member of, depending on
  step config);
- **on-call schedule** — resolved at fire time, so the right person gets
  paged based on who's actually on rotation right now (rather than who
  was on rotation when the policy was authored);
- **notification connection** — fan out to a specific channel
  (`integration_connection`);
- **all_admins** — every admin user in the org.

Policies are bound *to checks*: a check has zero or one active policy.
Resolution order is check's own → check group's → none
(`resolveEscalationPolicyUID`, `service.go:1267`). On
`incident.created`, `scheduleEscalationPolicy` (`service.go:1222`)
calls `ScheduleEscalationCycle`
(`server/internal/jobs/jobtypes/job_escalation_step.go:493`) which
creates one delayed job per step. The first step's delay is 0 by
convention but nothing enforces it — `delayMinutes=0` just means "fire
as soon as the job queue picks it up".

Repeating policies (`repeatMax > 0`, `repeatAfterMinutes > 0`) restart
the cycle from step 0 once the last step fires, up to `repeatMax`
iterations. The clock for the repeat is the *previous cycle's last fire*,
not the incident-open time.

Severity filtering: a step can specify a severity that narrows
deliveries to a channel-type set. A target whose channel-type isn't in
the set is skipped with an audit log entry rather than re-routed
(`fanOutWithSeverity`, `job_escalation_step.go:172`).

The cancel-on-ack/snooze invariant is the heart of the design: every
step's job config carries `{ incidentUid, stepUid, policyUid, ... }` and
the cancel sweep matches on `incidentUid`. Add a new field to the config
without breaking the cancel and you've created a paging bug — write a
test for it before merging.

## On-call schedules

A schedule resolves to "who is the on-call user **right now**" via
either daily or weekly rotation, with an explicit handoff time and
timezone, plus time-bounded user **overrides** (e.g. PTO coverage).

When an escalation step targets a schedule, the resolution happens at
*fire time*, not at policy-author time. This is intentional: the policy
captures intent ("page on-call EU") and the schedule captures the
on-the-ground rotation that may have changed since the policy was last
edited. The "page-now" preview in the dashboard shows you who would get
paged if the incident opened right now.

## Metrics emitted along the way

Prometheus counters/gauges feed into the dashboard at every stage; they
are not gated by incident state.

| Stage | Metric | Where |
|---|---|---|
| Probe completes | `solidping_check_executions_total{check_type,status,region,organization}` | worker fan-out |
| Probe completes | `solidping_check_duration_seconds` (histogram) | worker |
| Probe completes | `solidping_scheduling_delay_seconds` (histogram) | worker |
| Probe rate-limited | `solidping_checks_rate_limited_total{organization}` | `worker.go:511` |
| Incident created | `solidping_incidents_total{organization,check_type}` (counter) | incident service |
| Incident open/resolved | `solidping_incidents_active` (gauge ↑↓) | incident service |
| Check status | `solidping_check_up`, `solidping_check_status_streak` (gauges) | incident service |
| Worker fleet | `solidping_workers_active`, `solidping_worker_free_runners`, `solidping_worker_jobs_claimed_total` | heartbeat / claim paths |

## What this page does NOT cover

- Detailed REST surface: see [api-specification/notifications.md](../api-specification/notifications.md)
  and [api-specification/on-call.md](../api-specification/on-call.md).
- Exact JSONB schema of `integration_connections.settings`:
  per-channel-type, see the relevant `notifications/{slack,discord,…}.go`.
- Email-as-channel vs email-as-passive-check: those are different
  subsystems (passive checks live under `server/internal/handlers/emailcheck`
  and use JMAP). They share nothing except the word "email".
- Slack OAuth install flow: `server/internal/handlers/slackoauth/`.

## Known issues / planned changes

- **Manual resolve currently skips notifications.** Tracked in spec
  [2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md](../../specs/done/2026/05/2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md).
  Once that ships, the "which lifecycle events notify" table is fully
  accurate; today the manual-resolve row is aspirational for that path
  (the recovery-window-elapsed path already notifies).
- **Channel/connection naming alignment.** Tracked in spec
  [2026-05-07-03-align-channel-and-connection-naming.md](../../specs/done/2026/05/2026-05-07-03-align-channel-and-connection-naming.md).
