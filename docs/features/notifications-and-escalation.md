# Notifications & escalation

How a check failure becomes a Slack message, an email, and (if nobody
acks) a 3 AM page to whoever's on call this week.

This page is the end-to-end view a contributor needs before touching the
incident or notifier code. For per-resource API details see
[api-specification.md](../api-specification.md). For the data model see
[database-model.md](../database-model.md).

## The pipeline

```
check result ─┐
              ▼
       incident state machine ── PagingSuppressed? ── yes → no fan-out (rolled-up child)
              │                          │
              │                          no
              ▼                          ▼
       events table ◀──────── emitEvent (incidents/service.go:872)
                                         │
                       ┌─────────────────┴─────────────────┐
                       ▼                                   ▼
            queueNotifications                  queueGroupNotifications
            (single-check incident)            (group incident: fan one
                                                 message per (channel, type))
                       │                                   │
                       └────────── notification job ──────┘
                                         │
                                         ▼
                                  notifier dispatch
                                 (Slack / Discord / email
                                  / webhook / ntfy / …)
```

Two parallel branches feed off `EventTypeIncidentCreated`:

1. **Notifications** — fan out *now* to every channel bound to the check
   (or, for group incidents, to the union of every currently-failing
   member's channels). One job per (channel, eventType) pair so
   each Slack channel only buzzes once even if the underlying group spans
   ten checks.
2. **Escalation cycle** — if the check has an escalation policy attached,
   `scheduleEscalationPolicy` (`incidents/service.go:913`) creates one
   delayed job per step. Steps fire on the policy's cumulative
   `delayMinutes` schedule until the incident is acked, snoozed, resolved,
   or the policy's `repeatMax` is reached.

Resolved and reopened events fan out to channels but **do not** start a
new escalation cycle: resolved is final; reopened is a relapse and the
original cycle's pending steps were either already fired or canceled by
the ack/snooze that came before.

## Which lifecycle events notify?

The dispatcher in `emitEvent` (`incidents/service.go:884`) is explicit
about who notifies:

| Event | Channels notified | Escalation cycle | Notes |
|---|---|---|---|
| `incidentCreated` | yes | starts | The page-out moment. |
| `incidentEscalated` | yes | (in flight) | Each escalation step emits this. |
| `incidentResolved` | yes | canceled | Closes the loop with the channels that were paged. |
| `incidentReopened` | yes | does not restart | Relapse signal; the original cycle's pending steps were canceled when the previous ack/snooze landed. |
| `incidentAcknowledged` | no | canceled | Operator ack is silent on channels by design. Pending escalation steps are dropped. |
| `incidentUnacknowledged` | no | does not restart | Manual revert of an ack. |
| `incidentSnoozed` | no | canceled until the snooze expires | Operator silences the incident for a duration; expired snoozes get swept by `SweepUnsnooze` (`incidents/service.go:1976`). |
| `incidentUnsnoozed` | no | does not restart | The sweep emits this when the snooze window closes. |
| `incidentEscalationFailed` | no | — | Soft signal that a step couldn't deliver (audit only). |

The "no notifications on ack/snooze" decision is intentional: at 3 AM you
already know you acked your own page; channel members don't need an
"acked!" buzz. If you need that signal, watch the events stream.

## Group incidents

When several checks in the same `check_group` fail at once, the system
opens a single **group incident** instead of one per check. `emitEvent`
detects this via `incident.CheckGroupUID != nil` and dispatches through
`queueGroupNotifications` (`incidents/service.go:938`).

The dedup rule:

- For `incidentCreated` / `incidentEscalated` / `incidentReopened`:
  fan **one** notification per (channel, eventType) where the channel
  set is the *union* of every currently-failing member's bound channels.
  Recovered members do not bring their channels into mid-incident events.
- For `incidentResolved`: include channels from *every* member so that
  every channel that fired at open time hears about the close, even if
  that specific member recovered earlier in the incident's life.

## Suppression

Three independent layers can suppress notifications. They compose: any
one of them firing kills the page-out.

### 1. Maintenance windows

When a check is inside an active maintenance window, `ProcessCheckResult`
(`incidents/service.go:75`) returns *before* the state machine runs.
There is no incident, no event, no notification. The check status still
updates and results are still recorded; they just don't trigger paging.
When the window ends, the next failing result will open a fresh incident
if the failure persists. There is no "deferred" incident that fires at
window end.

### 2. Cascade dependency rollup

If the failing check has a hard parent dependency that is itself failing,
the new incident is created with `PagingSuppressed = true`. The event
still gets recorded (operators want to see the rolled-up child in the
timeline), but `emitEvent` skips the channel fan-out for everything
*except* the resolved event (`incidents/service.go:888`). When the parent
recovers, the suppressed child either inherits the parent's resolved
fan-out or fires its own resolved depending on whether it had also
recovered. See [check-dependencies.md](check-dependencies.md) for the
graph rules.

### 3. Per-incident silencing (ack / snooze / manual resolve)

Operators can ack, snooze, or manually resolve an incident at any time.
All three paths call `cancelPendingNotifications`
(`incidents/service.go:1610`) which soft-deletes any pending job whose
config carries the matching `incidentUid`. **This catches both
notification jobs and escalation step jobs** — the cancel sweep keys on
config and doesn't care which job type it is. The invariant is pinned by
[`TestCancelPendingForIncidentDropsEscalationSteps`](../../server/internal/handlers/escalationpolicies/runtime_test.go).

Snooze adds a `notBefore` argument so jobs scheduled inside the snooze
window are canceled but jobs scheduled to fire *after* the window are
left alone.

## Channels (a.k.a. connections)

The notification target is stored as an `integration_connections` row
in the database and called a "channel" everywhere a user sees it.
The split is a known papercut tracked in spec
[2026-05-07-03-align-channel-and-connection-naming.md](../../specs/todos/2026-05-07-03-align-channel-and-connection-naming.md).

A channel:

- has a `type` (one of nine: slack, discord, email, webhook, googlechat,
  mattermost, ntfy, opsgenie, pushover) that picks the dispatcher;
- carries type-specific config in a JSONB column with secret fields split
  into a `settings_private` envelope (see the encryption-at-rest section
  of `CLAUDE.md`);
- is bound to checks individually via `check_connections`. A channel
  marked `is_default = true` is auto-bound to every newly created check.
  Existing checks are not touched when the default flag flips.

A channel-bind is the smallest unit of "who hears about which check". The
escalation policy is the larger unit — it can target a channel, a user,
an on-call schedule (resolved at fire time), or all org admins.

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
On `incidentCreated`, the runtime calls `ScheduleEscalationCycle`
(`server/internal/jobs/jobtypes/job_escalation_step.go:398`) which creates
one delayed job per step. The first step's delay is 0 by convention but
nothing enforces it — `delayMinutes=0` just means "fire as soon as the
job queue picks it up".

Repeating policies (`repeatMax > 0`, `repeatAfterMinutes > 0`) restart
the cycle from step 0 once the last step fires, up to `repeatMax`
iterations. The clock for the repeat is the *previous cycle's last fire*,
not the incident-open time.

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

## What this page does NOT cover

- Detailed REST surface: see [api-specification.md](../api-specification.md).
- Exact JSONB schema of `integration_connections.settings`:
  per-channel-type, see the relevant `notifier/channel*.go`.
- Email-as-channel vs email-as-passive-check: those are different
  subsystems (passive checks live under `server/internal/handlers/emailcheck`
  and use JMAP). They share nothing except the word "email".
- Slack OAuth install flow: `server/internal/handlers/slackoauth/`.

## Known issues / planned changes

- **Manual resolve currently skips notifications.** Tracked in spec
  [2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md](../../specs/todos/2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md).
  Once that ships, this page's "which lifecycle events notify" table is
  fully accurate; today the manual-resolve row is aspirational.
- **Channel/connection naming alignment.** Tracked in spec
  [2026-05-07-03-align-channel-and-connection-naming.md](../../specs/todos/2026-05-07-03-align-channel-and-connection-naming.md).
