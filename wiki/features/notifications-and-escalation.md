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
                                   ▼
                          queueNotifications
                          (always per-check)
                                   │
                            notification job
                                   │
                                   ▼  jobs/jobtypes/job_notification.go:79
                            sender.Send(...)
                            (slack / discord / email
                             / webhook / ntfy / …)
```

Two parallel branches feed off `EventTypeIncidentCreated`:

1. **Notifications** — fan out *now* to every channel bound to the check.
   One job per `(channel, eventType)` pair, so a Slack channel bound to a
   check only buzzes once per event.
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

`routeCheckResultWithIncident` splits two ways on `isFailure`. Every
result takes the same path — a check that belongs to a check group is an
ordinary check as far as incidents are concerned (spec 2026-08-24-14), so
it opens its own incident through `createOrReopenIncident`, including
`applyRollup`.

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
| `incident.acknowledged` | `AcknowledgeIncident` (web/Slack/Discord/Telegram/email magic link/phone) | yes — `queueAckNotifications` (spec `2026-08-24-01`) | canceled |
| `incident.unacknowledged` | `UnacknowledgeIncident` (`POST /incidents/:uid/unack` — dash0, CLI, API) | yes — `queueUnackNotifications` (spec `2026-08-28-07`) | **resumes from the interrupted step** |
| `incident.snoozed` | `SnoozeIncident` | no | canceled until snooze expires |
| `incident.unsnoozed` | snooze cleared, or `SweepUnsnooze` (`service.go:2151`) when window closes | no | does not restart |
| `incident.comment` | `addCommentByOrgUID` (dashboard, API, Slack `/comment`, Telegram `/comment`, Discord `comment`, Slack/Discord thread reply in `all` mode) | yes — see below | — |
| `incident.escalation_failed` | a step couldn't deliver (no on-call user, empty schedule) | no | — |
| `incident.rolled_up` | `rollUpExistingChildren` — a hard parent opened AFTER this child and retroactively suppressed it | no | remaining steps skipped at fire time |
| `check.created/updated/deleted` | check CRUD endpoints | no | — |
| `org.activation.*` (5 milestones) | activation funnel (`internal/activation/`) | no | — |

Full enum: `db/models/event.go:12-56`.

Snooze and unsnooze stay silent on purpose: at 3 AM you already know you
snoozed your own page, and channel members do not need a buzz for it. If you
need that signal, watch the events stream.

**Acknowledgment is the exception, in both directions** — see
[the ack/unack/comment reach matrix](#ackunackcomment-reach) below.

### Ack/unack/comment reach

Two destination classes, and they are NOT the same set. **Channels** are the
connections attached to the incident's check (Slack, Discord, email, webhook…).
**Paged people** are person contacts (Telegram today) that only ever hear from
an escalation step.

| Event | Check-attached channels | People paged by escalation policy |
|---|---|---|
| `incident.acknowledged` | ✅ `queueAckNotifications` | ✅ `incident_ack_notice` job |
| `incident.unacknowledged` | ✅ `queueUnackNotifications` | ✅ `incident_unack_notice` job |
| `incident.comment` | ✅ `queueCommentNotifications` | ✅ `incident_comment_notice` job, one per comment |
| `incident.resolved` | ✅ `queueNotifications` | ✅ `incident_resolution_notice` job |

Everything in the unacknowledged row is spec `2026-08-28-07`; the comment row's
right-hand column is the same spec closing what the comment fan-out shipped as
a deliberate v1 gap.

#### Decision `2026-08-24-01` "unack is silent" is SUPERSEDED

Spec `2026-08-24-01` recorded, as a resolved decision, that **unack sends no
notification of its own** — rationale: "rare operator action". Spec
`2026-08-28-07` reverses it. Recorded here rather than quietly overwritten,
because the reasoning is what generalizes:

- Rarity argues for a cheap implementation, not for withholding information. A
  wrong belief held by five on-call engineers costs the same whether it is
  created once a month or once a day.
- The ack fan-out's own doc comment is the argument, inverted: an ack must be
  announced because otherwise "the four people who were woken up have no way to
  learn that the fifth already picked it up". Withdrawing that ack leaves four
  people believing an incident is owned when it is not.
- The decisive part was never the missing message. Slack and Discord rewrite the
  incident's **own alert card** in place when it is acked. Silence left that
  card — the canonical message for the incident, not a notification scrolled out
  of view — asserting "Acknowledged by Alice" forever.

#### What an unack now does

On a real transition (past the `AcknowledgedAt == nil` early return), and only
when the incident is still open and paging is not suppressed — the same guards
as the ack fan-out:

1. **Reverts the in-place rewrites.** Slack `chat.update`s the stored
   `message_id` and Discord edits the stored `discordKeyMessageID` embed back to
   an unowned, actionable card with the Acknowledge button restored. No storage
   change was needed for either: both ids were already persisted for the
   resolved/reopened edits.
2. **Fans a notice out** to the same connection set the alerts reached,
   gated by `notifications.AcceptsEventType`. `incident.unacknowledged` reuses
   the **existing** `NotifiesAcks` capability flag rather than adding a third
   one: a channel that opted out of "someone took it" is out of "they gave it
   back" by the same cost argument, and two flags could drift into a state where
   a channel hears only one half of the pair.
3. **Tells the paged people** through an `incident_unack_notice` job, threaded
   onto the same anchors, read rather than consumed.
4. **Resumes escalation from the step the ack interrupted** (see below).

There is deliberately **no echo-origin skip**: unack has exactly one entry
point (`POST /orgs/:org/incidents/:uid/unack`), and no chat platform ships an
unacknowledge button, so there is no surface whose message was already rewritten
in place. Adding one later means adding the skip with it.

**PagerDuty sends nothing for an unack.** Events API v2 accepts
`trigger`/`acknowledge`/`resolve` and nothing else, so there is no
un-acknowledge to send — and `trigger` is not a stand-in, since it would either
re-open a resolved PD incident or page the same rotation the unack is handing
the incident back to. Moving a PD incident from acknowledged back to triggered
is a REST API v2 operation and this integration holds a routing key, not an API
token. Note `PagerDutySender.Send`'s default branch IS `trigger`, which is why
the unack case is listed explicitly rather than left to fall through.

#### Escalation resume — decision `2026-08-28-07`

Three options were on the table: (a) chat notice only, (b) restart the cycle
from step 1, (c) resume from the step the ack interrupted. **(c) shipped.**

Unack means the incident is genuinely unowned again, so paging must continue —
announcing "nobody has it" while the system itself has stopped paging converts a
silent failure into a loud one that still depends on a human noticing a chat
message. But it resumes rather than restarts, so undoing a mis-click does not
replay pages that already fired.

The mechanism needs **no new column**. The ack's sweep
(`jobsvc.CancelPendingForIncident`) SOFT-deletes the pending escalation-step
jobs, and a soft-deleted row keeps its config (`stepUid`, `repeatIndex`,
`isLastStep`) and its `scheduled_at`. Those rows already are an exact record of
where the cycle stood; they were simply never read back.
`jobsvc.ListCanceledPendingForIncident(incidentUID, jobType)` returns them
(status still `pending`, `deleted_at` set), oldest due first. A step that had
already FIRED is in a terminal status and is therefore never in the set — which
is what makes "no replay" structural rather than a filter someone has to
remember.

Unack then de-duplicates by `(repeatIndex, stepUid)` — an ack → unack → re-ack
cycle leaves two canceled generations behind — and re-creates each job with its
original config verbatim, shifted as a block by
`max(0, now - earliest due time)`. A rung that fell due during the
acknowledgment fires immediately; a rung still in the future keeps the wait it
had left; the policy's own spacing between the remaining rungs is preserved. If
the canceled set is empty (no policy, or the cycle had run itself out) nothing
is scheduled: **unack never STARTS an escalation that was not running.**

Call order is load-bearing: the resume and both notices run AFTER
`cancelPendingNotifications`, or the sweep would cancel what they just created.

### Comment fan-out (spec `2026-08-15-08`)

`incident.comment` is the one notifying event that does NOT travel through
`emitEvent`. `addCommentByOrgUID` writes its own event row and calls
`queueCommentNotifications` directly, because the fan-out needs two things
`emitEvent` never carries: the comment body, and the connection the comment came
from. The `emitEvent` switch keeps an explicit (empty) `incident.comment` case
so it cannot be re-read as "comments are silent" — which is the bug this
replaced.

The connection set is identical to the lifecycle path (`ListChannelsForCheck`).
Two filters apply on top:

1. **Registry opt-out** — `notifications.AcceptsEventType(connType, eventType)`
   (`notifications/registry.go`). The per-sender table declares
   `NotifiesComments`; only `twilio` sets it false, because an SMS or a phone
   call per operator note is noise the recipient pays for. Everything absent
   from the table, including email, gets the default (all on). Call sites never
   name an integration type, which is what keeps the opt-out in one place.
2. **Echo suppression** — `isCommentEchoOrigin`. A comment carries
   `EchoOriginTeamID` (Slack) or `EchoOriginGuildID` (Discord), set ONLY by the
   thread-reply ingest path, and every connection in that workspace/guild is
   skipped. Matching is at workspace/guild level, not per connection row,
   because the incident's thread is stored once per incident: any connection in
   that workspace would post into the very thread the author is reading. The
   `/comment` slash command and Discord's `comment` command deliberately leave
   the echo origin empty — a command posts nothing visible, so the channel that
   typed it must still receive the fan-out.

   This is distinct from the bot-author ingest guard (`bot_id` on Slack,
   `author.bot` / `webhook_id` on Discord), which stops our own posts being
   re-ingested. Both are needed: one prevents an echo, the other prevents a loop.

The comment body travels in `NotificationJobConfig.Comment`
(`notifications.CommentInfo{Text, AuthorName, Source}`) rather than being
re-read from the event row at send time, so a job renders exactly the text that
was commented. Audit rows are written exactly as for lifecycle events.

**Comments also reach the people the escalation policy paged** (spec
`2026-08-28-07`, closing the v1 gap). `queueCommentNotifications` additionally
queues one **`incident_comment_notice`** job per comment. Before this, someone
woken by a Telegram page who was on none of the check's channels got the page,
the ack notice and the resolution notice — and never a word of the discussion in
between.

**One job per comment, enqueued immediately.** No batching, no coalescing
window, no throttle. The notification-noise cost the original v1 scope call was
avoiding is real — a chatty incident does buzz a phone per comment — and was
accepted deliberately: a merged digest is a different message from the
conversation people are actually having, and the recipients are the ones the
system judged important enough to wake up. The bound that does apply is the
existing hourly Telegram runaway guard inside the job.

The job's per-chat marker is scoped to the COMMENT
(`telegram_commented:<incidentUID>:<commentEventUID>:<chatID>`), not to the
incident. An incident-scoped marker — the discipline the ack notice uses — would
suppress every comment after the first, silently collapsing the feature back
into "one forwarded comment per incident".

### Closing the loop with person contacts

The table above is about **channels**. Person contacts (Telegram today;
WhatsApp/SMS are the planned follow-ups) are never reached by
`queueNotifications` — only the escalation step pages them, and it stops firing
the moment the incident is acked or resolved. Left there, the on-call engineer
gets a red Telegram alert and is never told it ended.

`incident.resolved` therefore also queues one **`incident_resolution_notice`**
job (`jobtypes/job_incident_resolution_notice.go`), enqueued after the
`PagingSuppressed` early return, so rolled-up children stay silent. The job:

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

## Group incidents — historical only

Until v0.18.0, several checks in the same `check_group` failing at once
produced ONE **group incident** with a `"<group> — N/M checks down"` title,
keyed to whichever member failed first, the rest recorded as
`incident_member_checks` rows. That is gone (spec 2026-08-24-14). Old rows
still render; nothing creates them.

It was removed because the consolidation cost more than it bought:

- a member that was not the group's first failure had no incident with its
  own `check_uid`, so its check page showed nothing and
  `GET /incidents?checkUid=` could not find it;
- it inherited the trigger member's notification and escalation state, so a
  core check going down 25 minutes into a stale incident never paged afresh;
- dependency rollup matches parents on `incidents.check_uid`
  (`findRollupRoot`), so a grouped check could never act as a rollup parent
  unless it happened to fail first — during the outage that motivated this,
  55 dependents paged individually for a cause that was already known.

**Notifications are now per-check, always.** The accepted trade-off is that a
correlated infra event taking down a group's prod *and* nonprod members
produces one incident per member rather than one merged incident — deliberate:
prod and nonprod deserve distinct paging.

The consolidated view survives where it is a presentation concern rather than
an identity one:

- **dash0** groups the incidents it has loaded by the check's
  `check_group_uid` and renders a plain `RabbitMQ — 2/6 down` header above the
  member rows (`web/dash0/src/lib/incident-grouping.ts`). No API change —
  `GET /incidents` stays a flat list.
- **Status pages** consolidate at the publication layer
  (`incidentpublications`): the first member to publish owns the public entry,
  later members append a rate-limited "also affecting X" note, and the entry
  closes when the LAST member recovers.

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

- has a `type` (slack, discord, email, webhook, googlechat, mattermost,
  msteams, msteams-bot, matrix, ntfy, pagerduty, pushover, webpush, twilio,
  kubernetes, freebox) that picks the dispatcher. Note that a type does not
  always imply one delivery mechanism: a `discord` integration is in **bot
  mode** or **legacy webhook mode** depending on whether its settings carry a
  guild and a channel, and the sender branches on the data rather than on a
  second connection type — see [../discord/README.md](../discord/README.md);
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
- Discord bot operator setup (application creation, bot permissions, the
  privileged `MESSAGE_CONTENT` intent, the two inbound transports):
  [../discord/README.md](../discord/README.md).

## Known issues / planned changes

- **Manual resolve currently skips notifications.** Tracked in spec
  [2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md](../../specs/done/2026/05/2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md).
  Once that ships, the "which lifecycle events notify" table is fully
  accurate; today the manual-resolve row is aspirational for that path
  (the recovery-window-elapsed path already notifies).
- **Channel/connection naming alignment.** Tracked in spec
  [2026-05-07-03-align-channel-and-connection-naming.md](../../specs/done/2026/05/2026-05-07-03-align-channel-and-connection-naming.md).
