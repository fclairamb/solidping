---
model: sonnet
effort: medium
---

# Slack can only be connected through the OAuth app install; add a `slack-webhook` integration

## Problem
The only way to send alerts to Slack today is the OAuth bot install (`startSlackInstall`). It needs an operator-configured Slack app (`SP_SLACK_CLIENT_ID`, `SP_SLACK_CLIENT_SECRET`, `SP_SLACK_SIGNING_SECRET`) and a workspace admin who approves the install.
- `web/dash0/src/routes/orgs/$org/integrations.new.tsx:196-225` makes Slack install-only: a manually created integration shows a "no linked Slack workspace" CTA and nothing else.
- `SlackSender.parseSettings` (`server/internal/notifications/slack.go:101-108`) fails with `ErrSlackAccessTokenNotConfigured` when there is no bot token.

Self-hosted users and anyone who cannot install an app have no way to post to Slack. Mattermost and Google Chat already work by pasting an incoming-webhook URL.

The Discord webhook transport was deliberately retired in favour of the bot (`e89710ddc`). This spec does not reverse that. It adds a separate one-way Slack webhook type and leaves the `slack` bot integration untouched.

## Proposal
Add a new connection type `slack-webhook`, distinct from `slack`, the same way `msteams` (one-way Workflow webhook) coexists with `msteams-bot` (`server/internal/db/models/integration.go:33-37`). An org can use either or both.

1. `server/internal/db/models/integration.go:17`: add `ConnectionTypeSlackWebhook ConnectionType = "slack-webhook"` with a comment mirroring the `msteams` / `msteams-bot` one. Make it notify-capable and not a source (see the `CanNotify` / `CanSource` cases in `integration_test.go:18-20`).
2. New `server/internal/notifications/slackwebhook.go`: `SlackWebhookSender` with settings `{ "webhook_url": "..." }` (snake_case, same tag convention as `mattermost.go:94-97`).
   - Copy the `Send` shape of `mattermost.go:57-68`: `ValidateSenderURL` with the egress guard, POST, status check, `webhookURLWithLegacyFallback`.
   - Body is Slack's incoming-webhook payload, `{"text": ..., "blocks": [...]}`. Reuse the block builders of `slack.go` (`buildIncidentFields` line 519, `checkNameLink` line 271, `incidentRefLink` line 259, `formatDuration` line 347) rather than duplicating them. Extract them to free functions if they need the `SlackSender` receiver.
   - No threads (a webhook returns no `ts`), no interactive buttons (`buildIncidentActionButtons`, line 607, needs the app), no mentions of Slack user ids. Every event (created, resolved, escalated, reopened, comment, ack, unack) posts a standalone message that carries the incident ref.
   - Missing URL returns a new `ErrSlackWebhookURLNotConfigured`.
3. `server/internal/notifications/registry.go:100`: register `models.ConnectionTypeSlackWebhook: func() Sender { return &SlackWebhookSender{} }`.
4. `server/internal/handlers/integrations/service.go`: add the type to `validConnectionTypes` (line 480) and `models.ConnectionTypeSlackWebhook: "webhook_url"` to `senderURLSettingsKey` (line 568), so the URL is validated at create and update time. Optionally require the host to be `hooks.slack.com` (see Open questions).
5. `server/internal/crypto/credentials/conn_secrets.go:21-25`: webhook URLs are intentionally not sealed. Add `slack-webhook` to that comment's list. No secret field.
6. OpenAPI, `server/internal/app/openapi/openapi.yaml`: add `slack-webhook` wherever the connection type enum is listed.
7. Dashboard:
   - `web/dash0/src/api/hooks.ts:5321`: add `"slack-webhook"` to `ConnectionType`.
   - `integrations.new.tsx:42`: add it to `ALL_TYPES` right after `"slack"`.
   - `web/dash0/src/components/integrations/integration-form.tsx:377`: a "Webhook URL" field, same as `mattermost` / `msteams`.
   - `integration-icon.tsx:71`: the Slack icon. `web/dash0/src/lib/channel-labels.ts:27`: label "Slack (webhook)".
   - Strings in every `web/dash0/src/locales/*/integrations.json` and `common.json`. Check `design-reference.tsx` first.
   - In the `slack` not-connected panel (`integrations.new.tsx:196-225`), add a line pointing to "Slack (webhook)" for users who cannot install the app.
8. Docs: `web/docs/docs/configuration/notifications.md`: a row in the table at line 14 and a "Slack (webhook)" section after the Slack one (line 130). How to create an incoming webhook in Slack, and what it cannot do compared to the app (threads, buttons, slash commands, comment capture, on-call mentions). Add a `CHANGELOG.md` entry per `wiki/conventions/changelog.md`.

## Tests
- `server/internal/notifications/slackwebhook_test.go`:
  - each event type POSTs to an `httptest` server with a `text` that contains the check name and incident ref;
  - resolved and comment events post standalone messages (no state lookup, no skip);
  - missing URL returns `ErrSlackWebhookURLNotConfigured`;
  - non-2xx response returns an error;
  - private or loopback URL is rejected by the egress guard;
  - legacy `webhookUrl` key is still read.
- `server/internal/notifications/registry` test: `GetSender("slack-webhook")` returns a `*SlackWebhookSender`.
- `server/internal/db/models/integration_test.go:18`: a `slack-webhook` row, `canNotify: true`, `canSource: false`.
- `server/internal/handlers/integrations/` service test: creating a `slack-webhook` integration with a public URL succeeds. A private URL or no URL fails with `VALIDATION_ERROR`.
- `web/dash0/e2e/`: create a "Slack (webhook)" integration from the new-integration page, save, reopen, the URL is shown. An invalid URL shows the validation error.

## To verify
- Every other place that enumerates connection types (severity channel allowlists, `regions/capabilities.go`, `channel-labels.ts`, MCP tool schemas, the integration list filters). Grep for `ConnectionTypeMSTeams` and `"msteams"` and add `slack-webhook` wherever `msteams` appears, unless the place is bot-specific.
- Whether severity channels match by connection type string. `models.DefaultSeveritySeeds` (`severity.go:101`) routes to `slack` only. Decide if an org that uses only `slack-webhook` gets alerts on the `default` and `critical` severities (see Open questions).
- Whether the DB has a CHECK constraint or enum on connection type that needs a migration (both Postgres and SQLite, see `wiki/conventions/migrations.md`).

## Resolved open questions
- Restrict the webhook URL to `https://hooks.slack.com/`. Validate it on create and update. It stops the type from being used as a generic webhook and catches pasted Workflow-builder or wrong URLs early.
- Add `slack-webhook` to `DefaultSeveritySeeds` next to `slack`, for new orgs only. No backfill of existing orgs.
