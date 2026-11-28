---
model: opus     # escaping on every chat sender, security-relevant output encoding
effort: medium
---

# Chat notifications interpolate check names, comments and failure text into markdown unescaped

## Problem

Found by the security scan of 2026-11-28 (finding BA1). Severity: Low. Who can trigger it: an org member with
the `user` role (check names, incident comments), or the operator of a monitored endpoint whose text reaches the
failure reason (for example the certificate names quoted in a TLS handshake error,
`server/internal/checkers/checkssl/checker.go:361`).

The Slack, Mattermost and Discord senders build their messages by string concatenation into each platform's
markup, with no escaping of the values they insert:

- Mattermost Blocks (new in #1085): `mattermostLink` (`server/internal/notifications/mattermost_blocks.go:181`)
  returns `"[" + text + "](" + url + ")"` with the raw check name. `mattermostTitleWithCheckLink` (`:163-177`)
  and the field blocks (`:237`, `:262`) wrap titles and values in `**…**` as is. The values come from
  `MattermostSender.buildFields` (`server/internal/notifications/mattermost.go:311-313`, `:341-354`): check name,
  comment author and text, and `getFailureReason` (`server/internal/notifications/slack.go:343`, which reads
  `incident.Details.failure_reason` / `output`, `server/internal/db/models/incident.go:263-275`).
- The legacy Mattermost attachment format renders the same fields as markdown.
- Slack: `slackLink` (`server/internal/notifications/slack.go:313-318`) formats `<url|text>`. A check name holding
  `>` ends the link, and the rest of the name can open a second `<url|label>` token. Slack documents that
  `&`, `<` and `>` must be sent as `&amp;`, `&lt;`, `&gt;`.
- Discord: field values and descriptions (`server/internal/notifications/discord.go:545-641`) render markdown,
  masked links included, for the check name and comment text.
- The Mattermost action callback (`server/internal/handlers/mattermost/handler.go:270-286`) puts the
  request's `user_name` into the re-rendered card ("Acknowledged by @…") and the ack trail with no length cap
  or escaping. A genuine Mattermost username cannot hold markup, but the field is not authenticated beyond the
  signed action token.

Only the Matrix sender escapes every dynamic value (`server/internal/notifications/matrix.go:227-254`).

Impact: a value can rewrite the visible link of an alert card. For example the check title link can point to a
look-alike login page next to the "Acknowledge" button, in an urgent card the on-call reader trusts. The
attacker gains no access by itself. This is a phishing aid inside the org's own alert channels. A `user` member
can also inject mentions or formatting into channels they cannot post to directly.

## Proposal

1. Add one escaper per markup in `server/internal/notifications` (for example `markup_escape.go`):
   - `escapeSlackText`: `&` → `&amp;`, `<` → `&lt;`, `>` → `&gt;`.
   - `escapeMattermostMarkdown` and `escapeDiscordMarkdown`: backslash-escape `\ [ ] ( ) * _ ~ ` > # | @`
     (check each platform's list), and neutralise `<` so autolinks and `<@id>` / `<!channel>` mentions cannot
     form.
2. Escape every untrusted value at the point it is inserted, not the URLs SolidPing builds itself:
   - Mattermost: the `text` argument of `mattermostLink`, the title prefix and field values in
     `mattermostTitleWithCheckLink` and `mattermostFieldBlocks`, and the legacy attachment fields.
   - Slack: the `text` of `slackLink` and every check name, comment, actor name and failure reason placed in
     mrkdwn.
   - Discord: field values, descriptions and the ack/comment actor names.
   Keep the markup SolidPing adds on purpose (`**`, the link around the check name).
3. `server/internal/handlers/mattermost/handler.go`: cap `user_name` and `user_id` (64 runes), trim them, and
   escape the name in `mattermostMention` / `mattermostActor` before it reaches the card or the ack event.
4. Failure reasons: cap the inserted text (for example 500 runes, with an ellipsis) so a long target-controlled
   string cannot fill the card. Leave the stored incident details unchanged.

## Tests

- `server/internal/notifications/mattermost_blocks_test.go`: a check named
  `API](https://evil.example) [x` renders a title holding exactly one link, whose URL is the dashboard
  check URL, and the name shows literally. Positive control: a plain name still renders `[name](checkURL)`.
- `server/internal/notifications/slack_test.go`: a check name holding `>` and `<https://evil.example|x>`
  produces no second link token (`&lt;` / `&gt;` in the payload). Positive control: a plain name keeps
  `<checkURL|name>`.
- `server/internal/notifications/discord_webhook_test.go`: a comment holding `[x](https://evil.example)` is
  escaped in the embed. Positive control: plain text is unchanged.
- A failure reason quoting the certificate names of a TLS error goes through the same escaping (Mattermost
  cause field).
- `server/internal/handlers/mattermost/handler_test.go`: a `user_name` with markdown and over 64 characters is
  capped and escaped in the ephemeral text and the updated card. A normal username still renders `@alice`.
- Each test fails before the fix.

## To verify

- Each platform's exact list of markdown metacharacters, and that escaping them renders literally there
  (Mattermost Blocks text, the legacy attachment fields, Discord embed fields).
- Other senders that build markdown from check names (Google Chat, Teams, Zulip, Telegram's parse mode): apply
  the same helper wherever they use markup, or note why they are safe.
