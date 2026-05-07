# BetterStack — Alerting Logic

How a confirmed failure becomes a page, who gets it, and what happens when nobody answers.

## Severities — the channel matrix

BetterStack normalizes "what channels do I use" into a `Severity` (a.k.a. `urgency`) primitive that escalation steps reference:

```
call · sms · email · push · critical_alert
```

`critical_alert` is iOS/Android push that bypasses mute / Do Not Disturb. Severities are referenced from any escalation step via `urgency_id` — it's the only field on a step that controls *which* channels fire.

## Escalation policies

The Terraform schema is the authoritative source — UI docs are sparse.

### Policy-level fields

- `name`, `policy_group_id`, `team_name`
- `incident_token` — a token for creating incidents directly against this policy via API
- `repeat_count` — "How many times should the entire policy be repeated if no one acknowledges the incident."
- `repeat_delay` — "How long in seconds to wait before each repetition."

Repeat is **per whole policy**, not per step. Cleaner than per-step repeats, which would explode the UI surface.

### Step types

A policy is an ordered list of `steps[]`. Four `type` values:

#### `escalation` — the "page someone" step

- `urgency_id` — points to a `Severity` resource (controls call/SMS/email/push/critical_alert at this step).
- `step_members[]` — typed targets:
  - `current_on_call`, `entire_team`
  - `user`, `webhook`, `slack_integration`, `microsoft_teams_integration`, `zapier_webhook`, `pagerduty_integration`, `policy` (chain into another policy), `incident_metadata` (resolve target from incident metadata at fire time)
  - Bulk forms: `all_slack_integrations`, `all_microsoft_teams_integrations`, `all_zapier_integrations`, `all_webhook_integrations`, `all_splunk_on_call_integrations`

The `incident_metadata` step-member type is the data plane for "if `severity = critical` jump to call-everyone policy" — see typed metadata below.

#### `time_branching` — schedule-based routing

Fields: `timezone`, `days` (`mon`..`sun` subset), `time_from`, `time_to`, `policy_id` (jump-to) **or** `policy_metadata_key` (resolve target policy from incident metadata at fire time).

Use case: "between 09:00 and 17:00 weekdays jump into the daytime policy; otherwise continue to the after-hours policy."

#### `metadata_branching` — data-driven routing

Fields: `metadata_key`, `metadata_value[]`, `policy_id` / `policy_metadata_key`.

Values are typed — they can be `String` or references to `User`, `Team`, `Policy`, `Schedule`, `SlackIntegration`, `LinearIntegration`, `JiraIntegration`, `MicrosoftTeamsWebhook`, `ZapierWebhook`, `NativeWebhook`, `PagerDutyWebhook`. **This typed-reference catalog is the architectural enabler.**

#### `instructions` — runbook on the page

Fields: `comment` (markdown, with `{token}` substitution and `- [ ] checklist` syntax that renders into the incident timeline as ticking checkboxes), `reminder_enabled`, `reminder_interval_hours` for periodic nags until the boxes are ticked.

**Worth borrowing**: the cheapest "runbook on the incident" feature we've seen.

### Per-step timing — relative, not cumulative

`wait_before` (Number, seconds): "How long to wait in seconds before executing this step **since previous step**."

So delays are *sequential* relative to the previous step (not absolute from incident-open). The visible "fire time" of step N is still the cumulative sum, but the field is per-edge.

Alternative to `wait_before`: `wait_until_time` + `wait_until_timezone` (`HH:MM`) — execute this step at the specified absolute time. Useful for "page nobody before 9 a.m."

### Cancellation on ack — explicit

> "After you have acknowledged an incident, no other team members get alerted anymore."

All pending escalation steps are cancelled, including the policy-level repeat. SolidPing's behavior matches this.

### Quick monitor-level shortcuts (legacy dual surface)

A monitor (and a heartbeat, and an incoming webhook) carries `email`, `sms`, `call`, `push`, `critical_alert` directly — Terraform doc gloss: "Whether to {channel} when a new incident is created."

When `policy_id` is **null**, these booleans are the channels used for the *first* on-call notification, plus `team_wait` controls when to fan out to "entire team" with the same channels.

When `policy_id` is **set**, the policy's per-step `urgency_id` wins. The monitor-level booleans become the *initial* notification (treated as an implicit "step zero" against the on-call) and feed the channel selection for any step that still references the monitor's defaults — but in practice the canonical pattern is "set `policy_id`, then ignore the monitor booleans."

**Don't replicate this dual surface in SolidPing.** The booleans pre-date severities; BetterStack tolerates the redundancy for back-compat. Pick one.

## On-call calendars

A schedule is a `betteruptime_on_call_calendar` with:
- `name`, `team_name`
- `default_calendar` (read-only — exactly one default per team)
- `on_call_users` (read-only, derived from the active rotation/event)

### Rotation block (`on_call_rotation`)

- `users[]` — emails
- `rotation_length` (integer)
- `rotation_interval` — one of `hour`, `day`, `week`. **No "month", no "custom".**
- `start_rotations_at` / `end_rotations_at` — RFC 3339 timestamps

### What's intentionally absent

**No native time-restriction** ("only weekdays 9-17"). To get "follow-the-sun", you build multiple calendars and use `time_branching` steps in the policy.

This is a deliberate split: **schedules are *who*, escalation policies are *when*.** Worth noting because it's a design decision, not an oversight.

### Events (overrides)

`POST /on-calls/{id}/events`:
- `starts_at`, `ends_at` — RFC 3339
- `users[]` — emails
- `override` (boolean) — "Override events take precedence over regular scheduled events."

This is the PTO mechanism. There is **no separate "override" object class** — just events with `override=true`. **Worth borrowing**: simpler than modeling overrides as a second class.

API constraint: `starts_at` "must be today at 0:00 UTC or later" — you cannot back-date events.

### Resolution timing — at fire time

`current_on_call` resolves at **fire time**, not at policy-create time. Swap the rotation 30 seconds before an incident opens, the new person is paged. (Inferred from the events API, which only requires the override to exist when the step executes.)

### Holiday mode — per-user

A per-user feature distinct from a schedule override: pauses *only* alerts to one person, useful when a single user is unreachable but the schedule itself shouldn't move. Worth considering — it's a different shape from "remove from rotation".

## Acknowledgement, snooze, manual resolve

### Ack — forever until resolve

The doc is unambiguous: "After you have acknowledged an incident, no other team members get alerted anymore." There is **no time-bounded snooze in the standard ack flow**.

Three ack channels:
- Dashboard button
- **Press 1 on the phone call** (hanging up without pressing 1 is *not* ack and triggers the next step)
- "Acknowledge" button in the email (signed magic link)

API: `POST /api/v3/incidents/{id}/acknowledge`. Note the `v3` path while most other resources are `v2`.

### Manual resolve

Dashboard button or `POST /api/v3/incidents/{id}/resolve`. Manual resolve sets `resolved_by` to the actor.

### Snooze — not in the public API

Closest equivalents:

- **AI-powered "incident silencing"** — auto-snoozes alerts the user has previously rated as low-priority. Configured by a sensitivity slider; the model "improves with every incident". Opaque, no API.
- **"Screening alerts" pattern** — docs-recommended workaround: a second team with a *silent* escalation policy (zero steps); incidents land there, humans triage, and click "Escalate to" to forward into the real paging policy.
- Per-user **holiday mode** — pause *all* alerts to one person.

**SolidPing should not copy AI silencing v1.** Ship the deterministic version: an explicit `POST /incidents/$id/snooze {"durationSeconds": N}` plus the screening-alerts pattern as a first-class `incidentRouting: "page" | "silent"` flag.

### Visibility

Ack/resolve actions appear in the timeline as `comment` / `generic` items, visible to the whole team.

## Notification integrations

The `Severity` primitive controls native channels (call/SMS/email/push). Step members reach out to integrations:

| Integration | Setup | Notable |
|---|---|---|
| **Slack** | OAuth (workspace install). TF surface is the resulting `betteruptime_slack_integration` ID | Bidirectional: ack/resolve from a Slack message |
| **Microsoft Teams** | App from Teams Apps store, then "Connect your team" link. **Cannot be private channels.** | Bot-based; no manual webhook URL config |
| **Discord, Telegram** | Outgoing webhook with custom body template | Not first-class |
| **PagerDuty** | One-way send. `key` (PD routing key, Events API v2) + `severity` enum (`info`, `warning`, `error`, `critical`) | One BS PD integration = one routing key = one PD service. PD's dedup key is set per-incident, not configurable |
| **Splunk On-Call (VictorOps)** | First-class TF resource | One-way |
| **Opsgenie** | Outgoing webhook to Opsgenie's API | No first-class TF resource |
| **Zapier** | First-class step-member type (`zapier_webhook`) | One-way |
| **Jira** | First-class TF resource | Bidirectional — closing the Jira issue can resolve the incident |
| **Linear** | Surfaces in metadata reference types | "Create Linear ticket" with AI-suggested fix on the incident detail page |
| **SMS / Voice** | Built-in. SMS supports ~50 countries; voice uses `pronounceable_name` for TTS | No custom voice script |
| **Push** | Native iOS/Android app | `critical_alert` bypasses mute |
| **Email** | Built-in | "Acknowledge" button is a signed magic link |
| **Generic webhook** | `betteruptime_outgoing_webhook` resource | See [integrations.md](integrations.md) for templating |

Custom payloads are only available for `outgoing_webhook`. Native paging channels (call/SMS/email/push) have no template override.

## Incident grouping ("incident merging")

- Configured at the **team level**, not per-monitor or per-group.
- Single setting: a **timeframe** within which related incidents are merged. Defaults and allowed values are not documented publicly.
- Trigger semantics are time-window based: "incidents that are likely related will be grouped together" during the configured window. The "likely related" predicate is not committed to in writing — observed behavior suggests same monitor group + overlapping time, plus integration-aware grouping for Prometheus etc.
- Merged objects: each underlying incident remains its own row, linked by `incident_group_id`. The dashboard stitches the timeline; the data model is a flat join.
- Marketing adds "smart incident merging" (1-tap ack 30 simultaneous incidents) — that's an alert-routing affordance on top, not a separate data model.

SolidPing's group-incident correlation already addresses the same need via check-group rollup. The naming convention (`incident_group_id`) is worth aligning to for ecosystem familiarity.

## Sources

- https://betterstack.com/docs/uptime/severities/
- https://betterstack.com/docs/uptime/escalation-policies/
- https://betterstack.com/docs/uptime/api/escalation-policies-api-response-params/
- https://betterstack.com/docs/uptime/api/create-escalation-policy/
- https://betterstack.com/docs/uptime/getting-started-with-oncall-v2/
- https://betterstack.com/docs/uptime/api/create-on-call-calendar-rotation/
- https://betterstack.com/docs/uptime/api/create-on-call-calendar-event/
- https://betterstack.com/docs/uptime/api/on-call-calendar-api-response-params/
- https://betterstack.com/docs/uptime/acknowledging-an-incident/
- https://betterstack.com/docs/uptime/resolving-an-incident/
- https://betterstack.com/docs/uptime/incidents/screening-alerts/
- https://betterstack.com/docs/uptime/api/acknowledge-an-ongoing-incident/
- https://betterstack.com/docs/uptime/api/resolve-an-ongoing-incident/
- https://betterstack.com/docs/uptime/incident-grouping/
- https://betterstack.com/docs/uptime/microsoft-teams/
- https://betterstack.com/docs/uptime/api/single-slack-integration/
- https://betterstack.com/incident-silencing
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_policy.md
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_severity.md
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_on_call_calendar.md
