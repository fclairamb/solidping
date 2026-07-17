---
model: opus
effort: high
---

# Incident comments from the web UI and Slack thread replies

## Problem

During an incident, the humans dealing with it produce the most valuable
context — "restarting the pod", "this is the same root cause as yesterday",
"escalating to the on-call DBA" — but SolidPing has nowhere to put it:

- The incident detail page (`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`)
  shows a timeline of **system** events only. The `events` table
  (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:575`) is an
  append-only audit log with event types like `incident.created`,
  `incident.acknowledged`, `incident.resolved`
  (`server/internal/db/models/event.go:12-63`), but there is no free-text,
  user-authored entry type.
- In practice the discussion happens in the Slack thread under the incident
  notification. The Slack integration already stores the thread coordinates
  (`channel_id`, `message_id`, `thread_ts`) in `state_entries` under
  `incidents/<incidentUID>/slack/thread`
  (`server/internal/notifications/slack.go:38`, `storeThreadInfo` at
  slack.go:129-143) and posts resolve/reopen replies into it — but **inbound
  thread replies are never captured**: `DispatchEvent`
  (`server/internal/integrations/slack/events.go:18-44`) handles
  `app_mention`, `link_shared`, etc., and the app manifest
  (`wiki/slack/manifest-prod.json:68-74`) does not subscribe to channel
  `message` events at all.

Result: the chain of events for an incident is split between an incomplete
in-app timeline and an unsearchable Slack thread. Post-incident review has no
single place to reconstruct what happened and who did what, when.

## Proposal

Introduce **incident comments** as a new user-authored entry in the existing
incident timeline, ingested from two sources: the web dashboard and Slack
thread replies.

### Data model — comments are events

Reuse the append-only `events` table rather than adding a parallel
`incident_comments` table:

- New event type `incident.comment` in `server/internal/db/models/event.go`,
  with `ActorType` = `user` and a `Payload` carrying:
  - `text` — the comment body (plain text, reasonable max length, e.g. 4 KB)
  - `source` — `web` | `slack`
  - for `web`: `ActorUID` = the authenticated user's UID (existing column)
  - for `slack`: Slack attribution in the payload (`slackUserId`,
    `slackUserName`, `slackTeamId`, `slackTs`) since the author usually has no
    SolidPing user; `ActorUID` stays empty
- Append-only fits the "chain of events" goal: no edit/delete in this
  iteration (matches the audit-log semantics documented on the table).
- Dedupe inbound Slack messages on `(incident_uid, slackTs)` so Slack event
  redelivery (Events API retries, socket-mode reconnects) doesn't create
  duplicate comments.

### API

- `POST /api/v1/orgs/:org/incidents/:uid/comments` — authenticated dashboard
  users add a comment; body `{ "text": "..." }`; creates the
  `incident.comment` event and returns it. Wire in
  `server/internal/handlers/incidents/` (or `handlers/events/`) and register
  next to the existing incident action routes
  (`server/internal/app/server.go:840-851`).
- Reading needs no new endpoint: comments come back through the existing
  `GET /orgs/:org/incidents/:uid/events`
  (`server/internal/handlers/events/handler.go:76`). Make sure the events
  response exposes enough of the payload for rendering (text, source, Slack
  author display name).
- Update `server/internal/app/openapi/openapi.yaml` and
  `wiki/api-specification.md`.

### Slack ingestion — thread replies become comments

- Subscribe the Slack app to channel message events (`message.channels`, and
  `message.groups` for private channels) with the matching
  `channels:history` / `groups:history` scopes; update the manifests under
  `wiki/slack/` and document that existing installs must re-authorize for the
  new scopes.
- Handle the new event in `DispatchEvent`
  (`server/internal/integrations/slack/events.go`): only messages that are
  thread replies (`thread_ts` set and ≠ `ts`), ignoring the bot's own posts
  and message subtypes (edits, deletes, `bot_message`).
- Reverse lookup thread → incident: the existing mapping is keyed
  incident → thread. When `storeThreadInfo` writes
  `incidents/<uid>/slack/thread`, also write a reverse `state_entries` key
  (e.g. `slack/threads/<team_id>/<channel_id>/<thread_ts>` → incident UID) so
  an inbound reply resolves to its incident in one lookup. Replies in
  unrelated threads are silently ignored.
- Multi-org routing follows the existing inbound path (see
  `specs/done/2026/07/2026-07-05-02-slack-inbound-multi-org-routing-uninstall-fanout.md`);
  both the HTTP Events API and socket-mode transports funnel through
  `DispatchEvent`, so one handler covers both.
- Web-authored comments are **not** mirrored out to Slack in this iteration
  (avoids echo loops); noted as a possible follow-up.

### Dashboard

- Comments render inline in the existing incident timeline on
  `incidents.$incidentUid.tsx`, visually distinct from system events (author
  name + source badge "via Slack" where applicable), via
  `web/dash0/src/components/dashboard/event-display.tsx`.
- Add a comment composer (textarea + submit) on the incident detail page,
  posting to the new endpoint; new comments appear live through the existing
  `LiveEventsContext` events stream if it covers event creation, otherwise
  refresh the events query on submit.
- Follow the design reference (`/dash0/orgs/default/design-reference`) for the
  composer and timeline entries; must be usable on mobile.

### Testing

- Backend: table-driven tests for the comment endpoint (auth, validation,
  max length), the Slack message handler (thread resolution, bot/self-message
  filtering, subtype filtering, redelivery dedupe), and the reverse-mapping
  write in `storeThreadInfo`. Integration tests against the fake Slack server
  used by existing notification tests.
- Frontend: Playwright E2E — add a comment on the incident page, see it in
  the timeline.

## Open questions

- Should acknowledged Slack authors be mapped to SolidPing users when their
  Slack email matches an org member? (Nice for attribution; out of scope
  unless cheap.)
- Retention/PII: comments inherit the events table's lifecycle; confirm
  that's acceptable for user-generated text.

## Implementation Plan

Ordered steps (each an isolated commit). No DB migration is needed: comments
reuse the existing `events` table (JSON `payload` column, `incident_uid`,
`actor_uid`), and dedupe + reverse thread lookup reuse the existing
`state_entries` table via its atomic `SetStateEntryIfNotExists`
(`ON CONFLICT DO NOTHING`) primitive.

1. **Event model** (`server/internal/db/models/event.go`)
   - Add `EventTypeIncidentComment EventType = "incident.comment"`.

2. **Incident comment service** (`server/internal/handlers/incidents/`)
   - `AddComment(ctx, orgSlug, *AddCommentRequest)` (web, resolves slug) and
     `addCommentByOrgUID(ctx, orgUID, ...)` (shared core) that validate text
     (non-empty after trim, ≤ 4 KB), fetch the incident, create an
     `incident.comment` event (`ActorType=user`; `ActorUID` set for `web`;
     Slack attribution in payload for `slack`), publish `realtime.KindEvents`,
     and return the event.
   - `AddCommentFromSlack(ctx, orgUID, incidentUID, text, slackUserID,
     slackUserName, slackTeamID, slackTs)` on the interface Slack consumes.
   - New sentinels `ErrCommentEmpty`, `ErrCommentTooLong`.

3. **API handler + route** (`incidents/handler.go`, `app/server.go`)
   - `POST /orgs/:org/incidents/:uid/comments` → `AddComment`, returns the
     created event as an events-shaped DTO. Validation → 400, not-found → 404.

4. **Slack ingestion** (`integrations/slack/events.go`)
   - `case "message"` in `DispatchEvent` → `handleMessage`: ignore non-thread
     replies (`thread_ts` empty or == `ts`), subtypes (edits/deletes/
     `bot_message`), and bot posts (`bot_id != ""`). Reverse-lookup the thread
     → incident (global `state_entries`), dedupe on
     `slack/comments/<team>/<channel>/<ts>` via `SetStateEntryIfNotExists`,
     resolve author display name best-effort, then `AddCommentFromSlack`.
     Covers both HTTP Events API and Socket Mode (both funnel through
     `DispatchEvent`).

5. **Reverse thread mapping** (`notifications/slack.go`)
   - In `storeThreadInfo`, alongside the forward
     `incidents/<uid>/slack/thread` entry, write a global reverse entry
     `slack/threads/<team_id>/<channel_id>/<thread_ts>` →
     `{incident_uid, organization_uid}` (best-effort; a failure logs but does
     not fail the send). Global (org-nil) so a cross-org notification still
     resolves inbound.

6. **Manifests + docs** (`wiki/slack/manifest-{prod,dev}.json`,
   `wiki/api-specification.md`, `openapi.yaml`)
   - Add `message.channels` / `message.groups` bot events and
     `channels:history` / `groups:history` bot scopes; note existing installs
     must re-authorize. Document the new endpoint.

7. **Dashboard** (`incidents.$incidentUid.tsx`, `event-display.tsx`, hooks,
   i18n) — a Discussion card rendering `incident.comment` events distinctly
   (author + "via Slack" badge + body) plus a composer (Textarea + Button)
   posting to the new endpoint; `useAddComment` invalidates the events query
   (refetch fallback) and the page subscribes to the live `events` scope so
   Slack/remote comments stream in. Filter comment rows out of the raw Event
   Log to avoid duplication.

8. **Tests** — incidents service (auth/validation/creation, web + slack),
   Slack message handler (thread resolution, filtering, dedupe), reverse-map
   write, and a dash0 Playwright E2E for the composer.

### Decisions on open questions
- **Slack author → SolidPing user mapping:** out of scope (not cheap — needs a
  `users:read.email` lookup + org-member match). Attribution uses the Slack
  display name in the payload.
- **Retention/PII:** comments inherit the `events` table lifecycle; no new
  work. Noted in docs.
- **Outbound mirroring** of web comments to Slack is explicitly not built
  (avoids echo loops) — future item.
