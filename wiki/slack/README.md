# Slack app manifests

The SolidPing Slack app is configured from these manifests (paste into the
Slack app config UI, or manage via the App Manifest API):

- [`manifest-prod.json`](manifest-prod.json) — production app (`solidping.io`).
- [`manifest-dev.json`](manifest-dev.json) — development app (`solidping.k8xp.com`).

## Inbound thread replies → incident comments

Incident comments (spec `2026-07-17-04`) ingest Slack thread replies posted
under the bot's incident message and record them on the incident timeline. For
this the app subscribes to channel message events and needs history read scopes:

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
