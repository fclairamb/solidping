---
model: opus
effort: high
---

# Incident comments never reach the people handling the incident, and Slack turns every thread reply into a comment

## Problem

Two halves, same feature area:

**Outbound — comments are invisible outside the dashboard.** When someone
comments on an incident (web UI, or a Slack thread reply), the comment lands on
the incident timeline and nowhere else: `addCommentByOrgUID`
(`server/internal/handlers/incidents/service.go:2344`) writes the append-only
event and publishes a realtime hint for watching dashboards, and the event
notification router explicitly parks `EventTypeIncidentComment` in the
"no notifications" bucket (`server/internal/handlers/incidents/service.go:1420`).
So a responder who was paged on Telegram, or is watching the incident from a
different Slack workspace or email, never sees "I think the central DNS is
down". Comments should fan out to everyone handling the incident, the way
`incident.created` / `incident.resolved` / `incident.escalated` already do.

**Inbound — commenting from a notification surface is Slack-only and
implicit.** Today every human reply in a tracked Slack incident thread is
auto-ingested as an incident comment
(`server/internal/integrations/slack/events.go:243` → `AddCommentFromSlack`,
`service.go:2327`). That is BetterStack's default behavior and it
over-captures: casual triage chatter ("lunch?", "who's on call?") becomes
permanent incident-timeline content. Adding a comment from chat should be an
**explicit** act — something like `/comment I think the central DNS is down` —
with the ingest-everything mode available as an opt-in, not the default.
Telegram's bot already has `/ack`, `/incident`, `/incidents`
(`server/internal/handlers/telegramcb/handler.go:221-235`) but no way to
comment at all.

## Proposal

### 1. Fan comments out through the existing notification pipeline

- Move `EventTypeIncidentComment` out of the no-notify bucket in
  `handleEventNotification` and route it through `queueNotifications` /
  `queueGroupNotifications` (`service.go:1547` / `:1468`), producing the same
  per-connection notification jobs (`NotificationJobConfig.EventType` gains
  `"incident.comment"`, `server/internal/jobs/jobtypes/job_notification.go:42`)
  with the usual `incident_notifications` audit rows.
- Teach the senders in `server/internal/notifications/` to render the event:
  comment text + author + incident reference, using the per-event-type
  emoji/color scheme from spec 2026-08-15-02. Slack posts it as a reply in the
  incident's existing thread (the reverse-thread mapping already exists);
  Telegram/Discord/Teams/Mattermost/Google Chat/ntfy/webhook/webpush send their
  normal message shape; email includes it. **Exclude Twilio (SMS/voice)** by
  default — paging someone's phone for every comment is noise with real cost.
  Per-sender opt-out should live in one place (the registry), not be scattered
  as `if` checks in the fan-out.
- **Echo suppression:** a comment that originated from a connection must not be
  re-posted to that same connection. The comment payload already carries its
  source (`commentSource`, plus `slackTeamId`/`slackTs` for Slack —
  `service.go:2368-2384`); skip the connection whose settings match. Without
  this, the bot re-posts the author's own words into the thread they just typed
  them in. (True loops are already prevented: our own posts carry a `bot_id`
  and are dropped by the ingest guard at `events.go:249`.)

### 2. Make Slack comment ingestion explicit by default

- Add a per-connection setting on the Slack integration, e.g.
  `commentIngestion: "explicit" | "all"`, **default `explicit`**. `"all"`
  restores today's ingest-every-reply behavior for teams that want it.
  Surface the toggle on the integration edit page in dash0.
- Slack constraint to design around: a message starting with `/` is treated by
  the Slack client as a slash command — with no registered command it errors
  client-side and never posts, and with a registered `/comment` command the
  payload **does not include `thread_ts`**, so the command alone can't tell
  which thread it was typed in. Recommended shape:
  - Register `/comment [#N] <text>` as a real slash command in the app
    manifest (works over both HTTP and Socket Mode). Resolve the target
    incident: explicit `#N` reference wins; otherwise if the channel maps to
    exactly one active tracked incident use it; otherwise reply with an
    ephemeral error listing candidates. Confirm with an ephemeral ack.
  - In `explicit` mode, thread replies are simply not ingested (the
    slash command is the path). Keep the existing reverse-thread ingest code
    path — `"all"` mode and the dedupe machinery
    (`events.go:302`) stay as-is.
- Comments created via `/comment` fan out like any other (minus the echo
  suppression above).

### 3. Telegram `/comment`

- Add a `/comment [ref] <text>` bot command next to `ack`/`incident` in the
  command switch (`telegramcb/handler.go:221`), reusing the incident-ref
  resolution in `telegramcb/resolver.go` (same UX as `/ack`: explicit `#N` or
  the unambiguous single active incident; error listing candidates otherwise).
  Attribute the comment to the verified linked contact's user
  (`linkedContacts`, `commands.go:24`) — likely a new `CommentSource` +
  fields on `AddCommentRequest` (`service.go:2330`) mirroring the Slack ones.

### Out of scope / open questions

- MS Teams bot (`notifications/msteamsbot.go`) commenting — same pattern would
  apply; follow-up spec if wanted.
- Forwarding comments to escalation-paged **person contacts** (the
  `queueResolutionNotice` path, `service.go:1442`) as opposed to check-attached
  channels — v1 scopes to channels; note it in the docs.
- Whether email should be in or out of the default comment fan-out set —
  proposal says in, cheap to flip.

Docs: update the Slack and Telegram integration pages (commands list, the new
ingestion toggle) in `web/docs/` and `wiki/`.

## Resolved open questions

Every item in the section above is already settled by the spec. Restated here
as directives so there is nothing left to decide at implementation time:

> MS Teams bot (`notifications/msteamsbot.go`) commenting

**Decision:** Out of scope. Do not implement a Teams `/comment`; a follow-up
spec covers it if wanted. (Teams still *receives* the comment fan-out like any
other channel — only the inbound command is out of scope.)

> Forwarding comments to escalation-paged **person contacts** (the
> `queueResolutionNotice` path, `service.go:1442`) as opposed to check-attached
> channels

**Decision:** Out of scope for v1 — comment fan-out targets check-attached
channels only. Note the limitation in the docs, as the spec asks.

> Whether email should be in or out of the default comment fan-out set

**Decision:** Email is **in** the default comment fan-out set. Twilio
(SMS/voice) remains the only excluded sender, opted out via the registry.
