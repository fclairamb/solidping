# Hyperping — Alerting Logic

How Hyperping turns a confirmed outage into a notification, and how it separates internal paging from customer-facing communication.

## Notification channels

Documented channels:

- Email
- SMS (Twilio-backed, internal)
- Phone call (voice) — outbound from `+31 970 10 25 7204` (EU) / `+1 (424) 325-7650` (US)
- Slack — alert webhook **and** Slack Incident Manager bot, two distinct integrations
- Microsoft Teams
- Discord (webhook)
- Telegram (`@HyperpingAlertsBot`)
- Google Chat
- PagerDuty — auto-create + auto-resolve
- Opsgenie — auto-create + auto-resolve
- Webhook (custom outgoing)
- Twilio (BYO Twilio account for status-page SMS subscribers — distinct from internal SMS)
- Intercom (status-page → Intercom messenger)
- HelpDocs (sync of incidents)

### Quotas / rate limits

- **Phone calls: 1 call per 5 minutes per user**, regardless of incident count. Hard global throttle to prevent alert-storm spam billing.
- Healthcheck pings: 300/min per IP, 10/min per token.
- API: 401 / 429 returned; specific limits not publicly documented.

### Test mode

- **Webhook**: explicit "test buttons" for `check.down` and `check.up`.
- **Slack alerts**: send fake Down/Up alerts for testing.
- Other channels: not documented.

## Alert routing

Hyperping's routing model is unusual:

- Channels are **not** bound to monitors directly. The chain is `monitor → escalation_policy → step → channels`.
- A monitor has a single `escalation_policy` field (UUID, or `null` / `"none"`).
- A step in a policy can fan out to multiple channels and multiple recipients simultaneously.
- "Assigning a policy to a monitor overrides simultaneous channel notification" — i.e. without a policy, **all configured project channels alert at once**; with a policy, the policy controls fan-out.
- "Everyone" recipients resolve to project membership.
- No documented per-monitor channel binding outside of policies.
- Time-based / business-hours routing is *implicit*: build separate rotations with hour windows, and point different policies at them. There is no first-class business-hours field on a step.

## On-call schedules

A schedule contains one or more **rotations**, each with its own timezone:

- Rotation types: daily, weekly, custom intervals.
- Per-rotation fields: `name`, `timezone`, `users` (drag-drop ordered), `start date+time`, **`handoff time`** (daily flip moment), **`concurrent shifts`** (N people on at once), optional **`time restrictions`** (limits hours when rotation is "active").
- Unified calendar timeline view across rotations.
- **Override / substitution**: marketing claims yes; the docs do not detail the mechanism.
- Multi-timezone is a primary feature, not an afterthought.

## Escalation policies

- Sequential steps. Each step is `(channels[], delay_minutes_from_previous)`.
- Step 1 can be 0 minutes (instant). Example shown in docs: 0 / +5 / +15.
- A step can target an **individual user**, **"Everyone"** (all project members), or an **on-call schedule** (only currently on-call user(s) get paged).
- Channels per step: Email, SMS, Phone, Slack, Teams, etc.

### Ack model

The outage object exposes `acknowledgedAt` + `acknowledgedBy { uuid, email, name }`. Ack stops further escalation steps.

The outage carries `escalationPolicy.alertedSteps` and `escalationPolicy.totalSteps` so a downstream system can see how far the escalation got at any moment. *Notable*: this is the cleanest "where are we in the escalation" signal we've seen in any competitor — worth borrowing.

API:

- `POST /v2/outages/{uuid}/acknowledge`
- `POST /v2/outages/{uuid}/unacknowledge`
- `POST /v2/outages/{uuid}/resolve`
- `POST /v2/outages/{uuid}/escalate` — manual escalation jump

**Snooze: not documented.** **Max steps: not documented.** Resolution before next step cancels remaining steps.

## Outages vs Incidents — the two-table model

Hyperping has **two distinct concepts**, which is rare in this space:

### Outages (`/v2/outages`)
The **operational alert** object. Auto-generated from monitor failures.

Fields: `uuid`, `incidentNumber`, `startDate`, `endDate`, `durationMs`, `isResolved`, `statusCode`, `severity` (e.g. `critical`), `detectedLocation`, `confirmedLocations` (csv string), `description`, `protocol`, `acknowledgedAt`, `acknowledgedBy`, `monitor { … }`, `escalationPolicy { uuid, name, alertedSteps, totalSteps }`.

Filters: `status=all|ongoing|resolved`, `type=all|manual|monitor`. Manual create/delete supported.

### Incidents (`/v3/incidents`)
The **status-page-facing** object. Public communication only — localized titles, attached components, status pages, ordered `updates[]` array.

Update types: `investigating`, `identified`, `update`, `monitoring`, `resolved`. Field `type: "outage" | "incident"` distinguishes auto-published-from-outage versus manually-posted.

### Why this matters

Most competitors collapse "internal alert" and "customer comms" into one record. Splitting them gives:

- The on-call person sees an `Outage` object with raw monitor data and ack/escalate verbs.
- The customer sees an `Incident` with a polished, localized timeline.
- Different access controls, different retention policies, different APIs.

This is the most novel pattern Hyperping ships and is worth replicating in SolidPing — see [wiki/research/alerting-patterns.md](../../research/alerting-patterns.md).

## Auto-merging across monitors

**Not documented as a feature.** Each monitor outage is its own outage record. Marketing references "grouped alerts" but it appears to be UI-level grouping, not a real correlation engine. Behind on BetterStack here.

## Maintenance windows

Single-window resource with these fields:

- `name`
- `start_date` (ISO 8601), `end_date`
- `monitors: [uuid]`
- Optional `statuspages: [uuid]`
- Localized `title { en, fr, de, ru, nl, pl, se }` (HTML allowed)
- `updates[]` (also localized)
- `notificationOption: none | immediate | scheduled`
- `notificationMinutes` (default 60)

### Recurrence
Not in the API create schema. The maintenance docs page mentions "recurring windows (weekly, monthly, etc.) that automatically create multiple maintenance instances" — implemented as syntactic sugar that materializes N windows at create time. Daily not explicitly listed.

### Suppression scope (orthogonal flags)
This is the design idea worth borrowing:

- **Monitor pausing**: selected monitors stop pinging — no results recorded, no alerts.
- **Status-page notice**: posts maintenance notice to subscribers without affecting monitors.
- You can do either or both, independently.

### Gap
**No "alert-only suppression"** mode that keeps checks running and continues recording results but silences alerts. Pausing is the only way to silence; it costs the data series. SolidPing should ship the third option.

### Manual completion
`POST /v1/maintenance-windows/{uuid}/complete` ends a window early.

## Sources

- https://hyperping.com/docs/projects/notification-channels
- https://hyperping.com/docs/integrations/phonecall
- https://hyperping.com/docs/integrations/slack
- https://hyperping.com/docs/integrations/discord
- https://hyperping.com/docs/integrations/telegram
- https://hyperping.com/docs/integrations/teams
- https://hyperping.com/docs/integrations/opsgenie
- https://hyperping.com/docs/integrations/twilio
- https://hyperping.com/docs/integrations/intercom
- https://hyperping.com/docs/integrations/googlechat
- https://hyperping.com/docs/monitoring/escalation-policies
- https://hyperping.com/docs/monitoring/on-call
- https://hyperping.com/docs/api/outages
- https://hyperping.com/docs/api/outages/list
- https://hyperping.com/docs/api/incidents
- https://hyperping.com/docs/api/incidents/create
- https://hyperping.com/docs/api/maintenance
- https://hyperping.com/docs/api/maintenance/create
- https://hyperping.com/docs/maintenance/create-maintenance
