# Slack app manifests

The SolidPing Slack app is configured from these manifests (paste into the
Slack app config UI, or manage via the App Manifest API):

- [`manifest-prod.json`](manifest-prod.json) — production app, on the public
  `solidping.io` host.
- [`manifest-dev.json`](manifest-dev.json) — template for a second, development
  app. Its URLs use the placeholder host `app.dev.example.com`: replace it with
  wherever your dev instance is reachable before pasting. **A Slack app holds
  one set of request URLs**, so dev needs its own app — it cannot share the
  production one.

## Slash commands

The manifests register two commands, both routed to
`/api/v1/integrations/slack/command` and dispatched by
`slack.DispatchCommand` — the transport-agnostic entry point, so HTTP and
Socket Mode behave identically:

- `/check <url>` — creates an HTTP check.
- `/comment [#42] <text>` — appends an incident comment (spec `2026-08-15-08`).

A slash command's payload does **not** carry `thread_ts`, which is why
`/comment` resolves its incident from the channel (explicit `#42` → the single
active tracked incident in the channel → an ephemeral error listing the
candidates) rather than from the thread it was typed in. It answers ephemerally
and does NOT suppress the origin workspace on fan-out: the command posts nothing
visible, so the channel that asked for the comment must still receive it.

## Inbound thread replies → incident comments

Incident comments (spec `2026-07-17-04`) ingest Slack thread replies posted
under the bot's incident message and record them on the incident timeline.

**Default since spec `2026-08-15-08`: this is OFF.** Each integration carries
`settings.comment_ingestion`:

| Value | Behavior |
|---|---|
| `explicit` (default; also the meaning of an absent key) | Plain thread replies are ignored; only `/comment` creates a comment. |
| `all` | Every human thread reply under a tracked incident thread is ingested — the pre-2026-08-15 behavior. |

The mode is read per inbound message via `GetConnectionByTeamID` and **fails
closed**: an unreadable connection or unparseable settings are treated as
`explicit`, because guessing the other way writes private triage chatter into a
permanent, fanned-out incident timeline.

The scopes below are still required for `all` mode:

- **Bot events:** `message.channels` (public channels) and `message.groups`
  (private channels), in addition to the existing `message.im`.
- **Bot scopes:** `channels:history` and `groups:history`.

Only thread replies under a tracked incident thread become comments; the bot's
own posts, message edits/deletes, and replies in unrelated threads are ignored.

> **Re-authorization required.** Adding scopes to the manifest does not grant
> them to workspaces that installed an earlier version. Every existing install
> must re-authorize (re-run the OAuth install flow) before its thread replies
> are captured — Slack will not deliver `message.*` events, and history reads
> will 403, until the workspace re-consents to the new scopes.

## Direct messages → the support inbox (spec 2026-08-22-02)

A DM to the bot (`channel_type: "im"`) is captured in the instance support
inbox rather than dropped. It is handled **before** the thread-reply gate in
`handleMessage`, because a DM's first message has no `thread_ts` and would
otherwise be discarded by it. Channel and group traffic is untouched.

- **Bot event:** `message.im` — already in both manifests.
- **Bot scope:** `im:history` — already in both manifests, but until this spec
  it was **missing from `slackBotScopes` in `service.go`**, which is what the
  authorize URL actually requests. The manifests were never the blocker; the
  install request was.

> **Re-authorization required, again.** Slack does not grant new scopes to
> existing installs. Every already-connected workspace must re-run the OAuth
> install before its DMs reach the inbox. This was accepted deliberately when
> the spec's open question was resolved, on the condition that the state
> degrades cleanly and visibly rather than looking like an empty inbox:
>
> - `models.SlackSettings.DMCaptureAvailable()` reads the stored grant;
> - the dash0 integration page shows a "Direct messages are not being captured"
>   banner with a **Reinstall Slack app** button;
> - `Service.ReportDMCapability` logs one WARN at boot with the count and
>   publishes `solidping_support_dm_unavailable{channel="slack"}`.
>
> Nothing errors and nothing spams the logs while a workspace has not
> reinstalled — the DMs simply never arrive, which is why the three signals
> above exist.
