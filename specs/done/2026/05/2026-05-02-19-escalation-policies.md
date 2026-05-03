# Escalation policies

## Context

Today, when an incident opens, SolidPing fans out to every notification
connection wired to the failing check (`check_connections`) and stops there.
There is no:

- "Wait N minutes; if no acknowledgment, notify someone else."
- "Page the on-call user, then their manager, then the team channel."
- "Repeat until acked."

Existing notification channels (email, Slack, Discord, Webhooks, Google Chat,
Mattermost, Ntfy, Opsgenie, Pushover) and the new ack/snooze surface
(`2026-05-02-16-incident-ack-snooze.md`) and on-call schedules
(`2026-05-02-17-on-call-schedules.md`) are the building blocks. This spec is the
orchestration layer that ties them together.

This is the feature that makes SolidPing a tool teams *rely on* for
production paging, instead of feeding into Opsgenie/PagerDuty for the
escalation layer.

## Scope

In scope:
- Escalation policy entity: ordered steps, each with delay-from-previous,
  targets, repeat behavior.
- Targets: user, on-call schedule (resolves at fire time), notification
  connection (existing `check_connections` rows by reference), or "all admins".
- Step execution via the existing job system (one job per scheduled step).
- Cancellation on ack/snooze/resolve (uses the same hook the ack-snooze spec
  introduces).
- Per-check assignment: a check (or a check group) references an escalation
  policy; the existing `check_connections` continue to fire immediately on
  every event (they're the "broadcast" channel) and the policy fires on top
  for the *paging* dimension.
- Frontend: a policy editor and an "active escalation" view on the incident
  detail page showing what fired, what's pending.

Out of scope:
- Round-robin within a step (target multiple users in turn instead of all at
  once). Use multiple steps with delay 0 and individual user targets as a
  workaround. Add real round-robin in v2 if requested.
- Routing rules ("policy A for severity high, policy B for severity low").
  We don't have severity yet; ship without it.
- Conditional steps ("only escalate during business hours"). Honest tradeoff:
  this is what people *think* they want, but operators repeatedly tell vendors
  it causes incidents to slip. Default to "always escalate"; revisit only on
  real demand.

## Why escalation policies and not "smarter check_connections"

A reasonable alternative is to extend `check_connections` with timing:
"connection X fires at T+0, Y at T+5min". Don't do this. Reasons:

1. `check_connections` are per-check. Escalation policies are reusable across
   many checks ("our standard prod paging policy").
2. They mix two concerns — "always notify channel X" and "page on-call" —
   that operators want to evolve independently.
3. The cancellation-on-ack semantics are policy-level, not per-channel.

Keep `check_connections` as-is. Add escalation policies as a new orthogonal
concept. Documenting this distinction in the UI is half the design work.

## Data model

### `escalation_policies`

| Column | Type | Notes |
|---|---|---|
| `uid` | varchar(36) PK | |
| `organization_uid` | FK | |
| `slug` | text | URL-friendly id, unique per org |
| `name` | text | |
| `description` | text nullable | |
| `repeat_max` | int | 0 = no repeat, N = repeat the entire policy N times if still not acked. Default 0. |
| `repeat_after_minutes` | int nullable | Delay before each repeat. Required if `repeat_max > 0`. |
| `created_at` / `updated_at` / `deleted_at` | | |

`(organization_uid, slug)` unique.

### `escalation_policy_steps`

| Column | Type | Notes |
|---|---|---|
| `uid` | PK | |
| `policy_uid` | FK | |
| `position` | int | 0-based |
| `delay_minutes` | int | Delay from incident open (for position 0) or from previous step (for position > 0). 0 = fire immediately. |
| `created_at` / `updated_at` | | |

`(policy_uid, position)` unique.

Decision: delays are *between adjacent steps*, not absolute from incident
open. Reason: when an operator inserts a step in the middle ("add a 5-minute
warning before paging the manager"), they don't have to recompute every
downstream delay.

### `escalation_policy_targets`

| Column | Type | Notes |
|---|---|---|
| `uid` | PK | |
| `step_uid` | FK | |
| `target_type` | enum | `user` \| `schedule` \| `connection` \| `all_admins` |
| `target_uid` | varchar(36) nullable | UID of user / schedule / connection. NULL when `target_type = 'all_admins'`. |
| `position` | int | Display order within the step |

A step can target multiple things; they all fire in parallel when the step
runs.

### `checks` and `check_groups` (additions)

Add `escalation_policy_uid` (nullable FK) to both. If both check and group
have a policy, the check's wins. NULL means "no escalation policy" — i.e.,
broadcast `check_connections` is the only thing that fires.

### Migration

Three new tables + two `ALTER TABLE` columns. One migration file is fine.

## Step execution model

When an incident opens, the resolver picks a policy (check's, then group's,
then nil). If non-nil:

1. Load all steps in `position` order.
2. For each step, schedule a job in the existing job system (`models/job.go`):
   - `kind = "escalation_step"`.
   - `scheduled_at = incident.started_at + cumulative_delay_minutes` (sum of
     delays through this step).
   - `metadata = {incident_uid, step_uid, repeat_index: 0}`.
   - `cancel_check_incident_uid = incident.uid` (the same column the
     ack-snooze spec adds).
3. Save them all atomically with the incident.

When the job fires:

1. Re-load the incident. If acked, snoozed, or resolved → exit (the cancel
   sweep should have caught it; this is a belt-and-braces check).
2. Resolve targets:
   - `user` → fan out to *all* notification connections that target that
     user. Use existing channel routing logic.
   - `schedule` → call the on-call resolver at `time.Now()`. If it returns
     a user, treat as `user`. If `ErrScheduleHasNoUsers` or
     `ErrScheduleNotYetActive` → log a warning, emit
     `incident.escalation_failed` event, fall through to the next step.
   - `connection` → fire that specific connection.
   - `all_admins` → look up the org's admin members and treat each as `user`.
3. Emit an `incident.escalated` event with payload
   `{step_position, repeat_index, targets: [...]}` so the timeline shows what
   happened.

When the *last* step fires and `repeat_max > 0` and `repeat_index <
repeat_max`:

- Schedule the steps again with `scheduled_at = now() +
  repeat_after_minutes` (for step 0) and the same delay structure between
  steps. `repeat_index = previous + 1`.

If `repeat_max == 0` or repeats are exhausted: stop. The incident remains
open; broadcast channels continue to fire on each lifecycle event (per
existing behavior); but no more paging until ack/resolve.

### Cancellation

Reuse the column added in the ack-snooze spec
(`cancel_check_incident_uid`). On ack / snooze / resolve, the incident
service updates pending jobs to cancelled. Do *not* duplicate the
cancellation logic — extend the ack-snooze cancel sweep to also match
`kind = 'escalation_step'`.

### Test scenarios

`server/internal/handlers/escalationpolicies/service_test.go`:

- Single-step policy targeting a user: schedule one job at T+0; verify it
  fires, target receives notification.
- Two-step policy with 5-min delay: schedule two jobs; ack between them;
  verify the second job is cancelled.
- Schedule target with no users → log warning, emit
  `incident.escalation_failed`, do *not* skip subsequent steps. (Reasoning:
  if step 1's schedule is empty, you still want step 2 to fire.)
- `repeat_max = 2`, `repeat_after_minutes = 30`: verify cycle 0 → 30min later
  cycle 1 → 30min later cycle 2 → stop. Ack between cycle 1 and cycle 2:
  cycle 2 cancelled.
- Group + check both have a policy: check's wins.
- Policy is deleted while incidents using it are open: pending jobs continue
  to reference it. Decision: soft-delete preserves the policy row, jobs
  resolve targets correctly. *Hard* delete is forbidden if any open incident
  references it (return CONFLICT). Tested.

## API

`/api/v1/orgs/$org/escalation-policies`

- `GET ` — list with step counts and "used by N checks".
- `POST ` — create. Body:
  ```json
  {
    "slug": "prod-paging",
    "name": "Production paging",
    "repeatMax": 1,
    "repeatAfterMinutes": 30,
    "steps": [
      {
        "delayMinutes": 0,
        "targets": [
          {"type": "schedule", "uid": "<schedule-uid>"}
        ]
      },
      {
        "delayMinutes": 10,
        "targets": [
          {"type": "schedule", "uid": "<schedule-uid>"},
          {"type": "connection", "uid": "<slack-channel-conn-uid>"}
        ]
      },
      {
        "delayMinutes": 15,
        "targets": [{"type": "all_admins"}]
      }
    ]
  }
  ```
- `GET /$slug` — full policy with steps and targets.
- `PATCH /$slug` — update fields and/or steps. Replacing `steps` rewrites the
  whole step list — simpler than diffing positions, and policies are small.
  Existing pending jobs (for already-open incidents) continue to use the *old*
  step list — they store enough to fire correctly. New incidents pick up the
  new list.
- `DELETE /$slug` — soft-delete. Returns 409 if any open incident references
  it (see test scenarios).

`/api/v1/orgs/$org/checks/$check`: extend the existing check PATCH to accept
`escalationPolicyUid` (nullable). Same for check groups.

## Frontend (dash0)

### Routes

- `/orgs/$org/escalation-policies` — list view. Each row: name, "1st step
  fires immediately to <preview>", "used by N checks", actions.
- `/orgs/$org/escalation-policies/$slug` — editor. Visual: vertical timeline
  of steps, each with `+N min` marker, draggable target chips. "Add step"
  button between any two existing steps. "Repeat after N minutes" footer.
  Live preview pane on the right showing what would happen at T+0, T+10,
  T+20, etc., for an incident opened now.
- Check edit form: dropdown to pick an escalation policy. Empty option is
  valid. Show a one-line preview of the chosen policy's first step.
- Check group edit form: same dropdown.

### Incident detail page

Add a section "Escalation timeline" listing what fired and what's pending:

```
✓ 14:32:01  Step 1 — paged on-call (Alice)             via Email, Pushover
✓ 14:42:01  Step 2 — paged on-call (Alice) + #ops      via Email, Pushover, Slack
○ 14:57:01  Step 3 — page all admins                   pending
```

Color: green for fired, gray for pending, red for `escalation_failed`.

When the user acks, mark all pending steps as "cancelled (ack at 14:38)".

### "What if" preview on the policy editor

The preview pane on the policy editor uses local state (no API roundtrip) to
show an example. Helps catch mistakes like "step 2 has zero targets" before
saving.

### i18n

`escalation` namespace.

## Risks / unknowns to flag before coding

- **Timing precision**: jobs are scheduled at minute granularity in the
  current job system (verify in `models/job.go`). If precision is coarser,
  document it — operators will notice "5-minute delay" actually firing at
  4:30 to 5:30. Ideally tighten to second-level scheduling for this purpose,
  but only if it's a small change to the existing job framework.
- **Channel resolution for `target_type = "user"`**: today,
  `check_connections` are per-check, not per-user. We need a notion of
  "user's preferred channels" — at minimum email (every user has one) plus
  any per-user Slack/Discord/Pushover identities the OAuth/integration
  setup already exposes. Inventory what's available before designing the
  user-targeting code path. If only email is available cleanly, document
  that v1 user-targeting = email + any user-installed Pushover/Ntfy device,
  and that targeting Slack/Discord channels still goes through `connection`
  targets.
- **Org-wide vs check-specific defaults**: should there be an org-level
  "default escalation policy" so admins don't have to set it on every check?
  Probably yes. Add `escalation_policy_uid` to the `organizations` table
  (or to the existing org settings) and use it as the fallback after group.
  Add this to the spec only after the basic flow is implemented — easy to
  layer on, hard to walk back if the API shape is wrong.
- **Vocabulary collision** with adaptive incident resolution
  (`keyEscalationThreshold` in `incidents/service.go`). The two "escalations"
  *will* be confused. Concrete proposal: the adaptive-resolution mechanism
  should be renamed to "promotion" or "incident-state escalation" in code
  comments and UI labels; on-call escalation owns the word "escalation"
  going forward. Touch this in *this* spec's PR — same person likely
  writing both pieces of code, easier to land together.
- **Race condition**: a job fires at T+10min just as the user clicks ack. The
  cancel UPDATE happens after the worker has already claimed the job. Two
  options: (a) the worker re-reads the incident state inside the job and
  exits if acked (already in step 1 of the execution model — keep this),
  (b) accept that one extra page may go out (operationally fine, users
  understand). Keep both: the early-exit catches 99% of cases, the rest is
  acceptable.
- **Notification deduplication**: if a check has both
  `check_connections` and an escalation policy that targets the *same Slack
  channel* via a `connection` target, the channel gets two messages. Good
  default? Probably yes — they have different framing ("incident opened"
  broadcast vs. "step 2 escalation paging"). But document it; some users
  will complain. If a deduper is added later, it should be opt-in and live
  at the channel level, not the policy level.
