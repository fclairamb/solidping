---
model: opus
effort: high
---

# Telegram bot incident commands — status, list, ack (button-first), detail

## Problem

The Telegram integration is notify-only. The webhook understands exactly three
inbound commands — `/start <token>` (account linking), `/stop`/`unlink`, and
"anything else is ignored" (`server/internal/handlers/telegramcb/handler.go:157`).
Once an alert lands in a chat there is nothing the on-call person can *do* from
Telegram: no way to see what is currently down, no way to acknowledge, no way to
pull the latest state of an ongoing incident. Slack already has an interactive
"Acknowledge" button on its alert messages
(`server/internal/integrations/slack/interactions.go:220` →
`handleAcknowledgeIncident`), so Telegram is the odd one out.

A second, structural gap: incidents are identified only by UUID
(`server/internal/db/models/incident.go:30`). Nobody will type a 36-char UID
into a chat on a phone, so any typed command needs a human-scale reference.

## Proposal

### 1. Short per-org incident numbers (the ref ↔ UID link)

Add a **persistent, per-org, monotonically increasing `number` column** to
`incidents` — GitHub-issue style:

- `number BIGINT NOT NULL` + unique index on `(organization_uid, number)`,
  migrations for both PostgreSQL and SQLite
  (`server/internal/db/postgres/migrations/`, `server/internal/db/sqlite/migrations/`).
- Assigned at incident creation. Concurrency-safe on both engines: insert with
  `number = (SELECT COALESCE(MAX(number), 0) + 1 FROM incidents WHERE organization_uid = ?)`
  and retry on unique-violation (or a per-org counter row with
  `UPDATE … RETURNING`; implementer's choice, but it must work on SQLite too).
  Soft-deleted rows keep their number; numbers are never reused.
- Backfill existing incidents in the migration, ordered by `started_at`.
- Resolution is then a plain lookup: `#42` → `WHERE organization_uid = ? AND number = 42`.
  The mapping is durable (survives restarts, multi-instance, and time) —
  explicitly **not** an ephemeral "index into the last `/incidents` listing",
  which races with new incidents and needs per-chat state.
- Expose `number` in the incident API payloads and show `#42` in dash0 incident
  views, Slack messages, and Telegram messages — the ref is useful everywhere,
  not just in Telegram.

Note: inline-button callbacks do **not** need the short ref — callback data
carries the full UID (a 36-char UUID fits Telegram's 64-byte `callback_data`
limit). The number is for humans: display and typed commands.

**Alternative considered and rejected:** storing the mapping in `state_entries`
(`telegram_incident_nb:<n>` → uid, claimed via `SetIfNotExists`). Migration-free
and atomic, but the render path needs the reverse `uid → n` direction on every
alert message and listing line (forcing a second entry or a `details` stash with
no integrity guarantee), numbers become unqueryable (no `ORDER BY`, N+1 lookups
in listings), and a permanent identity assignment doesn't belong in a table
documented for transient notification state, tokens, and locks
(`server/internal/db/models/state.go:10`). `state_entries` **is** the right
place for the per-chat coordination this spec needs — see below.

### 2. Button-first acknowledge (primary path)

Mirror the Slack pattern on the Telegram alert message itself:

- Extend the Telegram client (`server/internal/integrations/telegram/client.go`)
  with inline-keyboard support on `SendMessage`/`EditMessageText` and an
  `AnswerCallbackQuery` method. Parse `callback_query` updates in
  `server/internal/integrations/telegram/webhook.go`.
- Alert messages (`BuildAlertHTML` in
  `server/internal/integrations/telegram/message.go`, sent from
  `server/internal/jobs/jobtypes/job_escalation_step_telegram.go` and the
  user-notification path) gain an **"Acknowledge"** inline button with
  `callback_data` like `ack:<incident-uid>`.
- On callback: ack the incident (same service path Slack uses), answer the
  callback query (toast), and **edit the original message** to show
  "✅ Acknowledged by <who> at <time>" with the button removed — the Telegram
  equivalent of Slack's message update + thread reply.
- The incident ↔ Telegram-message mapping needed for that edit lives in
  `state_entries`, mirroring the Slack thread-state pattern
  (e.g. `StateKey("incident", uid, "telegram_message")` → chat id + message id).
- Idempotent: acking an already-acked/resolved incident answers the callback
  with the current state instead of erroring.

### 3. Typed commands (fallback + read paths)

Route these through the existing command parser
(`server/internal/integrations/telegram/webhook.go:154`) and the switch in
`server/internal/handlers/telegramcb/handler.go:157`:

- `/status` — one-line org health: "✅ all N checks up" or
  "🔥 3 open incidents, 42/45 checks up".
- `/incidents` — open incidents, each as `#42 <check> — <severity>, open 23m`,
  with an Acknowledge button per unacked incident.
- `/ack [#ref]` — ack by number; **with no argument, ack the single open
  incident** (error listing candidates when there are several).
- `/incident <#ref>` — latest detail: state, duration, failing regions, last
  error, acked by/at.
- `/help` — command list; `/start` *without a token* on an already-linked chat
  shows the same help instead of the linking flow.
- Register the command list via Telegram's `setMyCommands` at bootstrap
  (`server/internal/app/telegram_bootstrap.go`) so commands autocomplete in the
  client.
- Commands only work in linked chats (existing `user_contact` linkage); an
  unlinked chat gets the "link your account" reply.

### 4. Ack attribution

- DM chats: the chat is linked to a user — `AcknowledgedBy` is that user.
- Group chats: the presser of the button may not be the linker. V1 decision:
  attribute to the linked contact's user but record the actor as
  `via Telegram (<first name>)` in the incident detail/timeline; a full
  Telegram-user-id → org-member mapping is out of scope for this spec.

## Scope notes

- `/mute` / `/snooze` and reply-threading of incident updates are follow-ups,
  not part of this spec.
- Tests: table-driven coverage for the number-assignment race (parallel
  creations, both engines), command parsing/routing, callback ack flow
  (including idempotency), and the unlinked-chat guard — following the
  existing patterns in `webhook_test.go` / `handler_test.go`.

## Implementation Plan

1. **`incidents.number` column (both engines).**
   - Scratch migration `012_incident_number.{up,down}.sql` in
     `server/internal/db/postgres/migrations/` and `.../sqlite/migrations/`
     (011 is frozen — shipped in v0.14.0). Adds `number BIGINT NOT NULL DEFAULT 0`,
     backfills per-org ordered by `started_at` (then `uid` as tie-break), and creates
     the unique index `incidents_organization_number_idx (organization_uid, number)`.
     Idempotent / re-runnable on both engines.
   - `models.Incident.Number int64`.
   - `CreateIncident` on both engines assigns the number in a retry loop:
     `SELECT COALESCE(MAX(number),0)+1 FROM incidents WHERE organization_uid = ?`
     (no `deleted_at` filter — soft-deleted rows keep their numbers and numbers are
     never reused), insert, retry on unique violation. Shared classifier for the
     unique violation so both engines behave identically.
   - New `db.Service.GetIncidentByNumber(ctx, orgUID, number)`.

2. **Surface `#42` everywhere.**
   - `IncidentResponse.Number` (`"number"`), `openapi.yaml`, regenerated
     `server/pkg/client/`.
   - dash0: `IncidentDetail.number`, rendered as `#42` on the incidents list and the
     incident detail header.
   - Slack incident messages and Telegram alert bodies carry `#42`.

3. **Telegram client / webhook plumbing.**
   - `client.go`: `InlineKeyboard` on `Message` + `EditMessageText`, plus
     `AnswerCallbackQuery` and `SetMyCommands`.
   - `webhook.go`: decode `callback_query` updates; add `callback_query` to
     `AllowedUpdates`.
   - `message.go`: `AlertParams.Number`, `AckKeyboard`, acknowledged-body builder,
     and the `/status`, `/incidents`, `/incident`, `/help`, not-linked bodies.

4. **Button-first ack.**
   - Alerts (escalation job) gain an `ack:<uid>` inline keyboard while the incident is
     open and unacked.
   - `telegramcb` handles `callback_query`: resolve the chat's linked contacts,
     ack via the incidents service (`AcknowledgeIncidentFromTelegram`), answer the
     callback with a toast, then EDIT the message the button was on (the callback
     carries chat id + message id, so no state lookup is needed) to
     "✅ Acknowledged by … at …" with the keyboard removed. Idempotent on
     already-acked/resolved.

5. **Typed commands.**
   - `/status`, `/incidents`, `/ack [#ref]`, `/incident <#ref>`, `/help`, and
     `/start` with no token on a linked chat → help.
   - Linked-chat-only guard (`ListUserContactsByTypeValue`), otherwise the
     "link your account" reply.
   - `setMyCommands` registered in `app/telegram_bootstrap.go`.

6. **Attribution.** DM → the linked contact's user UID. Group chat → same user UID, with
   `via Telegram (<first name>)` recorded on the ack event payload / timeline.

7. **Tests.** Parallel number-assignment race on BOTH engines, command parsing/routing,
   callback ack incl. idempotency and the unlinked-chat guard, message rendering.
