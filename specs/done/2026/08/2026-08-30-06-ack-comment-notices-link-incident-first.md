---
model: sonnet
effort: medium
---

# Ack / comment notices link to the check, but the context lives on the incident

## Problem

When an incident is acknowledged, unacknowledged, or commented on, the notices we
push to Slack (and some other mediums) hyperlink the **check**, not the
**incident**. Everything the reader wants at that moment — the timeline, who
acked, the comment thread, resolve/escalate actions — is on the incident detail
page; the check page is the secondary destination.

Concretely, in `server/internal/notifications/slack.go` every thread reply
renders `slackLink(checkURL, checkName)` as its only link:

- `buildAckThreadReply` — [slack.go:573](server/internal/notifications/slack.go:573)
- `buildUnackThreadReply` — [slack.go:590](server/internal/notifications/slack.go:590)
- `buildCommentThreadReply` — [slack.go](server/internal/notifications/slack.go) (`:speech_balloon: *%s* commented on %s%s` — the link after the `#42 · ` prefix is the check)
- the resolved and reopened thread replies follow the same pattern

Meanwhile `incidentRefPrefix` ([slack.go:244](server/internal/notifications/slack.go:244))
renders the incident number (`#42 · `) as **plain text** — the one token that
names the incident is the one thing not clickable.

Other mediums are inconsistent rather than uniformly wrong:

- Discord embeds already point their card `URL` at the incident
  ([discord.go:507](server/internal/notifications/discord.go:507), :742).
- MS Teams bot cards carry both links ([msteamsbot.go:453](server/internal/notifications/msteamsbot.go:453)-459).
- Matrix/Gotify link the incident.
- Google Chat / Mattermost comment & ack cards and the email templates
  (`email.go:602-606` exposes both `CheckURL` and `IncidentURL`) need a quick
  audit for the same check-first bias.

Both URL builders already exist side by side:
`checkDashURL` / `incidentDashURL` ([slack.go:257](server/internal/notifications/slack.go:257), :267).

## Proposal

For **incident-lifecycle notices** (ack, unack, comment — and, for consistency,
resolved/reopened thread replies), make the **incident the primary link** and
keep the check as an optional secondary link:

1. **Slack thread replies**: link the `#42` incident ref itself — turn
   `incidentRefPrefix(...)` + plain text into
   `slackLink(incidentURL, "#42") + " · "` (fall back to plain text when
   `incidentDashURL` returns `""`, same as today's empty-URL fallback in
   `slackLink`). The check name that follows can either keep its existing
   `checkDashURL` link (preferred — that's the "second link to the check" from
   the request) or, where a message has room for only one link, switch to the
   incident URL.
   - When `incident.Number <= 0` (no `#42` to anchor on), link the check *name*
     to the **incident** URL instead, so the notice always has an incident link.
2. **Audit the other mediums** that render ack/unack/comment notices
   (Google Chat, Mattermost, Zulip, ntfy, webpush, email templates, Telegram's
   `AlertParams` in `server/internal/integrations/telegram/`): wherever the
   notice's primary/only tap target is the check page, repoint it at the
   incident, keeping a secondary check link where the medium's format supports
   it (buttons/fields). Mediums that already lead with the incident (Discord,
   Matrix, Gotify, MS Teams bot) stay as they are.
3. **Do not touch the initial alert (incident-created) card layout** beyond
   what's needed for consistency — it already carries both links in its footer
   (`:warning: Incident` + `Monitor`, [slack.go:528](server/internal/notifications/slack.go:528));
   the fix is about the follow-up notices.
4. Update the corresponding tests (`slack_test.go`, `ack_test.go`,
   `unack_test.go`, and the per-medium tests touched by the audit) to assert
   the incident URL is the primary link and the check link survives as
   secondary.

Open question for the implementer: whether webhook/PagerDuty payloads should
change too — those are machine-consumed, so leave their field semantics alone
unless a field is literally documented as "the link to show a human".
