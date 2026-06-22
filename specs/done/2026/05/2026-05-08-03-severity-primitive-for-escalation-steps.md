# Add a `Severity` primitive for escalation steps

## Context

Every escalation step today *implicitly* fans out to whatever channels
its target carries. A step targeting a user pages whatever default
channels that user has configured (today: nothing — we don't model
per-user channel preferences). A step targeting a notification channel
pages exactly that one channel. A step targeting all admins fans out
through every admin's channels.

This means **the urgency of a page is encoded by which step you're on**,
not by an explicit field. "Try Slack first, then SMS, then phone call"
is expressed as three sequential steps with three different targets.
The pattern works but has friction:

- "Wake the on-call up" requires three separate steps (one per
  channel type) instead of one step with `severity = critical`.
- Adding a fourth channel (e.g. push notifications) means editing
  every existing policy.
- Customers who want "fan out simultaneously to email + SMS" can't —
  they have to pick one channel per step.

BetterStack's solution is a `Severity` resource (a.k.a. `urgency_id`)
that bundles channel choices:

```
Severity { id, name, channels: [call, sms, email, push, critical_alert] }
```

Each escalation step references a `severity_id`. The step targets
*who* gets paged (user / schedule / channel / all admins); the
severity decides *how* (which channels fire simultaneously for that
target). See
[`wiki/research/alerting-patterns.md §… escalation`](../../docs/research/alerting-patterns.md) and
[`wiki/competitors/betterstack/alerting.md §Severities`](../../docs/competitors/betterstack/alerting.md#severities--the-channel-matrix).

## Goal

Introduce a `Severity` resource scoped per-org, with full CRUD
endpoints and a default seed (`Default`, `Critical`, `Low`). Each
escalation policy step gains an optional `severityUid` field. When set,
the step pages its target via the severity's channel set, simultaneously,
on the same incident-event tick.

When `severityUid` is null, behavior matches today: the step's target
gets paged via whatever channels the target itself carries (back-compat,
no change for existing policies).

## Approach

### Data model

New table `severities`:

| Column | Type | Notes |
|---|---|---|
| `uid` | uuid PK | |
| `organization_uid` | uuid FK | |
| `slug` | text | unique per org, lowercase kebab-case |
| `name` | text | display label |
| `channels` | jsonb | `["email", "sms", "voice", "push", "critical"]` subset |
| `description` | text | optional |
| `is_default` | boolean | exactly one per org marked true |
| `created_at`, `updated_at`, `deleted_at` | standard |

`escalation_policy_steps` gets a new column:

| Column | Type | Notes |
|---|---|---|
| `severity_uid` | uuid FK NULL | optional, FK with `ON DELETE SET NULL` |

### Channel-set vocabulary

The channels in a `severities.channels` array are *channel-types*, not
channel UIDs. Severities are about "fire SMS to whoever this step
targets", not "fire to channel-uid-abc". The channel-types are:

- `email`
- `sms` — text message (gated on a future SMS provider integration)
- `voice` — phone call (gated on a future voice provider)
- `push` — mobile/desktop push (gated on a future apps shipping)
- `critical_push` — push that bypasses Do-Not-Disturb (matches
  BetterStack's `critical_alert`; same gating as `push`)
- `slack`, `discord`, `webhook`, etc. — same names as our existing
  `ConnectionType`. A severity can include `slack` to mean "if the
  target has a Slack channel configured, fire that".

The vocabulary is open per-org: validation rejects unknown values
against a shared enum derived from `ConnectionType` plus the new
direct-channel types (sms/voice/push/critical_push).

### Default severities seeded per org

When an org is created (or migrated), three severities are seeded:

| Slug | Name | Channels | Default? |
|---|---|---|---|
| `low` | Low | `[email]` | no |
| `default` | Default | `[email, slack]` | yes |
| `critical` | Critical | `[email, slack, sms, voice, critical_push]` | no |

Operators edit these, add their own, or delete any non-default. The
default severity gets used by escalation steps that omit
`severityUid` *and* whose target type is `user` or `all_admins` (the
only target types that need channel-resolution; `connection` targets
already specify their channel).

### Resolution at fire time

The escalation step runner
(`server/internal/jobs/jobtypes/job_escalation_step.go:160` —
`fanOut`) gains a severity-resolution path:

1. If `step.severityUid` is non-null: look up the severity row, take
   its `channels` array.
2. Else if step's target type is `user` or `all_admins`: use the org's
   default severity's channels.
3. Else (target type is `connection`): the connection itself decides
   the channel; severity is ignored.

For each channel-type in the resolved set, find the matching delivery
mechanism on the target:

- For a `connection` target: the connection itself is the delivery — fan
  out to it directly.
- For a `user` target: fan out across every channel-type the user has
  enabled (today: just email — see "Out of scope" below).
- For an `all_admins` target: fan out across every channel-type each
  admin user has enabled, deduped per (admin, channel-type) pair.

If a channel-type is in the severity but the target has no matching
delivery (e.g. SMS without a phone number on the user record), that
channel is silently skipped for that target. An audit-event records
the skip so operators can reconcile.

### Why severity sits outside the step rather than on each target

A step can have multiple targets today (`step.targets[]`). Putting
severity on each target would mean editing N rows when an operator
wants to "make the whole step critical". Putting severity on the step
matches the natural mental model: *steps* have urgency; *targets* are
who gets reached at that urgency.

## Files to add / edit

### Backend

- `server/internal/db/migrations/NNN_severities.up.sql` — create the
  `severities` table, add `severity_uid` to
  `escalation_policy_steps`. Seed three rows per existing org.
- `server/internal/db/models/severity.go` — new file with `Severity`,
  `SeverityUpdate`, factory `NewSeverity`.
- `server/internal/handlers/severities/` — new package with
  `service.go` and `handler.go`. Standard CRUD: `GET /list`, `POST`,
  `GET /:slug`, `PATCH /:slug`, `DELETE /:slug`.
- `server/internal/db/service.go` — add CRUD method signatures.
- `server/internal/db/sqlite/severity.go`,
  `server/internal/db/postgres/severity.go` — implementations.
- `server/internal/handlers/escalationpolicies/service.go` — accept
  the new `severityUid` field on step create/update.
- `server/internal/jobs/jobtypes/job_escalation_step.go` — the
  fan-out resolver outlined above.
- `server/internal/app/server.go` — wire the new severities handler
  group at `/api/v1/orgs/:org/severities`.

### MCP

- `server/internal/mcp/tools_severities.go` — new file mirroring
  `tools_connections.go`: `list_severities`, `get_severity`,
  `create_severity`, `update_severity`, `delete_severity`.
- `server/internal/mcp/tools.go` — register them.

### Frontend

- `web/dash0/src/api/hooks.ts` — `Severity` type, hooks for the new
  endpoint.
- `web/dash0/src/routes/orgs/$org/severities.index.tsx` and
  `severities.$slug.tsx` — list page and edit route (per the
  edit-on-route convention from spec 04).
- `web/dash0/src/components/escalation/step-target-row.tsx` — extend
  the step editor with a Severity select (above or alongside the
  Type/Target row).
- Sidebar entry under "Alerting" (or alongside escalation policies).
- `web/dash0/src/locales/{en,fr,de,es}/escalation.json` — add
  severity-related labels (`severity`, `severities.title`,
  `severities.empty`, etc.).

### Docs

- `wiki/api-specification.md` — document the new `/severities`
  endpoints and the `severityUid` field on escalation steps.
- `wiki/features/notifications-and-escalation.md` — extend the
  "Channels" and "Escalation policies" sections to describe how
  severities resolve at fire time.

## Out of scope

- **Per-user channel preferences.** A user can't yet say "I prefer
  Slack over email for low-severity pages". For v1, user targets fan
  out across every channel-type the user has configured globally
  (which today is just email). User preferences are a separate spec.
- **SMS / voice / push providers.** This spec defines the *vocabulary*
  for severity channel-sets but doesn't ship the actual delivery
  mechanisms for sms/voice/push/critical_push. Those gate behind
  follow-up specs (Twilio integration, Apple/Google push, etc.).
  Until those land, including them in a severity does nothing — the
  escalation step skips them with an audit event.
- **`critical_alert` (DnD bypass) flag separate from `push`.**
  Mobile push DnD bypass is iOS/Android specific and inside the
  push provider's scope. Model it as the channel-type
  `critical_push` (same delivery, different priority hint), not as
  a separate boolean on every channel.
- **Severity-driven incident grouping.** Two incidents at "critical"
  could conceivably auto-merge differently than two at "low". Out
  of scope for v1; the existing group-incident correlation continues
  to operate on check_group membership only.
- **AI-suggested severity.** BetterStack's "incident silencing"
  inferred severity from past user behavior. We're not doing that.

## Verification

1. `make build-backend lint-back test` clean.
2. New `severities` package tests cover CRUD, default-flag uniqueness
   (exactly one per org), slug uniqueness, channel-array validation
   (rejects unknown channel-types).
3. Escalation step fan-out test: a step with `severityUid` set to a
   severity containing `[email, slack]` and a user target with both
   email and a Slack channel configured fires *both* channels in the
   same tick. Verified by counting jobs created with matching event
   metadata.
4. Default seed: create a fresh org via the admin flow, confirm three
   severity rows land with the right channel arrays.
5. Manual smoke: edit an escalation policy in the dashboard, set a
   step's severity to `critical`, save, force-fail a check, observe
   that the step fires across all channels in the critical severity
   set.
6. MCP: `list_severities` returns the seeded rows; `create_severity`
   with bogus channel-type returns a `VALIDATION_ERROR` response.

## Implementation Plan

1. **Schema**: migration adds `severities` table + `severity_uid`
   column on steps. Down migration drops both.
2. **Model + service**: `Severity` model, CRUD service, default-flag
   invariant (exactly one default per org enforced via DB unique
   partial index `WHERE is_default AND deleted_at IS NULL`).
3. **HTTP handler**: standard list/get/post/patch/delete shape
   matching the existing `connections` package.
4. **Per-org seed**: extend the org-creation hook to insert the
   three default severities. Add a one-shot startup migration that
   seeds them for existing orgs.
5. **Step runner**: extend `fanOut` to consult severity → channel set
   → target's matching deliveries. Skip-with-audit for missing
   deliveries.
6. **MCP tools**.
7. **Frontend**: severities list/edit pages, step editor extension,
   i18n keys.
8. **Docs**: api spec + notifications-and-escalation features page.
9. Completeness audit, archive, merge.
