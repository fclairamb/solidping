---
model: opus
effort: high
---

# Slack DMs (test button, escalation pages, ops notify) and the Slack channel picker fail because the bot token now lives in `settings_private` and most readers only look at public `settings`

## Problem

On `https://solidping.k8xp.com/d/orgs/acme/account/notifications`, pressing the
send-test icon on the "Slack DM" route (`U05AL44K9GS`) calls
`POST /api/v1/orgs/acme/users/me/notification-routes/<routeUid>/test` and the
dashboard answers with the toast **"test failed: slack client not configured"**.

The org *does* have a working, installed Slack app. What the dev API returns for
its only Slack integration (`b69c17d8-…`, name "Acme", `isDefault: true`):

```json
{
  "settings": { "team_id": "T05AH5UE8KX", "bot_user_id": "U0BF713BSBF", "channel_id": "C0BFC2XG4K0", "…": "…" },
  "settingsPrivateKeys": ["access_token"],
  "updatedAt": "2026-09-04T15:48:26Z"
}
```

`access_token` is no longer in the public `settings` JSONB — it is in the
`settings_private` envelope. That is by design:
[`server/internal/crypto/credentials/conn_secrets.go:29`](../../server/internal/crypto/credentials/conn_secrets.go)
declares `access_token` as the Slack secret key, the integrations write path
splits it out unconditionally ("Secrets are ALWAYS split out of the public
`settings` column … in every mode",
[`server/internal/handlers/integrations/service.go:914`](../../server/internal/handlers/integrations/service.go)),
and the boot-time sweeps `credmigrate.Run` / `credmigrate.RunPlaintext`
([`server/internal/app/server.go:3840`](../../server/internal/app/server.go),
[`:3925`](../../server/internal/app/server.go)) move it for every pre-existing
row, master key or not.

But the Slack OAuth install flow still writes the token into public `settings`
([`server/internal/integrations/slack/service.go:639`](../../server/internal/integrations/slack/service.go),
`:806`, `:820`, `:1169`), and **almost every consumer of the bot token reads
`conn.Settings` only**, never opening `SettingsPrivate`. So a fresh install
works until the next server boot or the next PATCH from the edit page, after
which the token silently "disappears" for those readers. Only two code paths
decrypt correctly today:

- channel alerts — [`server/internal/jobs/jobtypes/job_notification.go:105-125`](../../server/internal/jobs/jobtypes/job_notification.go) (decrypt + `credentials.MergeConfig` before handing the connection to the sender);
- member identity sync — [`server/internal/handlers/integrations/identities.go:276`](../../server/internal/handlers/integrations/identities.go) via `loadDecryptedSettings`.

Everything else is broken once the split has happened. Confirmed readers of
`settings.AccessToken` that never look at `SettingsPrivate`:

| Reader | Symptom |
|---|---|
| [`server/internal/handlers/usernotifications/senders.go:70-78`](../../server/internal/handlers/usernotifications/senders.go) `SlackDMSenderAdapter.SendDMTest` | the reported toast `slack client not configured` (`dispatchTestSlack` at [`service.go:712`](../../server/internal/handlers/usernotifications/service.go) already has `s.creds` in hand but passes the raw row down) |
| [`server/internal/jobs/jobtypes/job_escalation_step.go:584-590`](../../server/internal/jobs/jobtypes/job_escalation_step.go) escalation Slack DM | **real pages are silently dropped** — logs `slack access token not configured; skipping route`, returns 0, the on-call human never gets the DM |
| [`server/internal/opsnotifywire/wire.go:137-140`](../../server/internal/opsnotifywire/wire.go) `sendSlackDM` | ops-notify DMs answer `errNoSlackToken` |
| [`server/internal/integrations/slack/service.go:1290-1302`](../../server/internal/integrations/slack/service.go) destinations listing | dashboard channel picker: `GET /orgs/acme/channels/b69c…/slack/destinations` → **409 `CHANNEL_NOT_CONNECTED` "install the Slack app"** on dev, for an app that is installed |
| [`server/internal/integrations/slack/service.go:1061-1066`](../../server/internal/integrations/slack/service.go) `GetClient` (events, slash commands, interactivity, thread replies) | `newAPIClient("")` → Slack answers `invalid_auth` |
| [`server/internal/integrations/slack/service.go:1139-1145`](../../server/internal/integrations/slack/service.go) `SetDefaultChannel` | same, welcome message and channel-name lookup fail |

The other `SlackSettingsFromJSONMap(conn.Settings)` call sites
(`events.go:348/463/554`, `service.go:231/716/1006`, `mentions.go:36`,
`ack_notice.go:152`, `incidents/service.go:3053`, `usernotifications/service.go:262`)
only read public fields (`team_id`, `mention_on_call`, `comment_ingestion`,
`team_name`) and are fine.

Two secondary issues surfaced while tracing this:

1. `GetSlackChannelForOrg` (both
   [`postgres/user_contact.go:340`](../../server/internal/db/postgres/user_contact.go)
   and [`sqlite/user_contact.go:340`](../../server/internal/db/sqlite/user_contact.go))
   is `LIMIT 1` with no `ORDER BY`, so an org with several Slack integrations
   pages through a nondeterministic one. It should prefer the default
   integration (`is_default`), then oldest first.
2. The error text is misleading: "slack client not configured" reads as an
   instance/config problem, and the escalation path just logs and skips. Once the
   token is read correctly this mostly goes away, but a genuinely token-less
   stub should say "Slack app not installed for this organization".

Dev pod logs for the last 14h show no `slack access token not configured` line
only because no escalation reached a Slack DM step in that window; the
destinations 409 above is the live proof.

## Proposal

**1. One shared way to read a connection's secrets.** Add a helper in
`server/internal/crypto/credentials` (name suggestion:
`OpenConnectionSettings(ctx, creds Service, conn *models.Integration) (map[string]any, error)`)
that is exactly the logic `job_notification.go:105-125` already implements:

- `SettingsPrivate` nil/empty → return a copy of `Settings` (pre-split rows keep working, no migration);
- plaintext envelope (`!RequiresKey`) → `OpenPlaintext`, merge;
- sealed envelope → requires `creds != nil && creds.Enabled()`, else a typed
  `ErrEncryptionDisabled`; `DecryptForOrg`, `MergeConfig(Settings, private)`;
- never mutate the input row (callers may cache it).

Then a thin Slack-specific wrapper, e.g. `slack.BotToken(ctx, creds, conn) (string, error)`
(or `models.SlackSettingsFromIntegration(ctx, creds, conn)`), returning a typed
`ErrSlackNotConnected` when the merged `access_token` is empty.

**2. Route every bot-token reader through it:**

- `usernotifications`: `dispatchTestSlack` decrypts with `s.creds` and passes the
  token (or merged settings) to `SlackDMSender.SendDMTest`; the adapter stops
  parsing `ch.Settings` itself. Keep the `SlackDMSender` seam so tests can stub it.
- `job_escalation_step.go:584`: use `jctx.Services.Credentials` like
  `job_notification.go` does; keep the "skip route" behaviour on a real
  missing token but log the decrypt error distinctly (a failed decrypt is an
  operator problem, not "not configured").
- `opsnotifywire/wire.go`: `sendSlackDM` receives the credentials service.
- `integrations/slack.Service`: add `credentials.Service` to `NewService`
  ([`server/internal/app/server.go:1949`](../../server/internal/app/server.go))
  and use the helper in `GetClient`, `SetDefaultChannel` and the destinations
  method (every `newAPIClient(settings.AccessToken)` call — `service.go:1066`,
  `:1145`, `:1302`).
- `handlers/integrations/identities.go:276` can switch to the shared helper
  (behaviour unchanged).

**3. Stop writing the token in public settings at install time.** The OAuth
install / re-install paths (`slack/service.go:639`, `:806`, `:820`, `:1169`)
should split via the same `ConnectionSecretFields` + `SplitConfig` +
seal/encrypt logic the integrations handler uses (extract it from
`handlers/integrations/service.go:900-950` into the credentials package so
both write paths share it). Until then, a freshly installed app works only
until the next boot, which is exactly the "it used to work" confusion.

**4. Deterministic org Slack channel.** `GetSlackChannelForOrg` (PG + SQLite):
`ORDER BY is_default DESC, created_at ASC LIMIT 1`.

**5. Clear errors.** `ErrSlackClientNotConfigured` → text "Slack app not
installed for this organization" (dashboard toast + docs), distinct from a
decrypt failure ("credentials encryption disabled — see SP_ENCRYPTION_MASTER_KEY").

### Tests (must prove the negative, with a positive control)

- Unit, `usernotifications`: a Slack integration whose `access_token` is
  **only** in `SettingsPrivate` (once as a plaintext envelope, once AES-sealed
  with a test master key) → `dispatchTestSlack` reaches the sender with the
  token. Control: token only in public `Settings` (pre-split row) still works.
  Negative: token in neither → `ErrSlackClientNotConfigured`.
- Unit, `jobtypes`: escalation Slack DM step posts the DM for a split row
  (today it returns 0 / "skipping route").
- Unit, `integrations/slack`: destinations listing and `GetClient` succeed for a
  split row; a genuinely token-less stub still returns `ErrSlackNotConnected`.
- Regression guard: a test that walks every `newAPIClient(` / `AccessToken`
  reader is overkill; instead assert in the credentials package that
  `ConnectionSecretFields(slack)` contains `access_token` **and** that the
  Slack install path stores a row with `settings->>'access_token' IS NULL`
  and `settings_private_keys = ["access_token"]`.
- DB: `GetSlackChannelForOrg` returns the default integration when two are
  enabled (PG + SQLite).

### Out of scope (note for a follow-up spec)

`freebox` ([`server/internal/app/server.go:4058`](../../server/internal/app/server.go)),
`kubernetes/client.go:153` and `twilioconn/resolve.go:39` each carry their own
copy of the decrypt-and-merge dance; once the shared helper exists they can be
migrated, but they are not broken today.

## Implementation Plan

1. **`server/internal/crypto/credentials/conn_settings.go`** (new)
   - `ErrEncryptionDisabled` — typed, message names `SP_ENCRYPTION_MASTER_KEY`.
   - `OpenConnectionSettings(ctx, creds Service, conn *models.Integration) (map[string]any, error)`
     — the `job_notification.go:105-125` logic, verbatim semantics, never mutating `conn`.
   - `SealConnectionSettings(ctx, creds Service, connType, orgUID, effective) (public, *envelope, *keysJSON, error)`
     — the `handlers/integrations/service.go:900-950` split/seal logic, extracted.
2. **`server/internal/integrations/slack/token.go`** (new) — `BotToken(ctx, creds, conn) (string, error)`
   and `Settings(ctx, creds, conn) (*models.SlackSettings, error)`, returning `ErrSlackNotConnected`
   on an empty merged `access_token`. The slack package is already imported by every bot-token
   reader (jobtypes, opsnotifywire, usernotifications, handlers/integrations), so one wrapper
   serves them all with no import cycle.
3. **Readers routed through the helper**
   - `handlers/integrations`: `applySettingsEncryption` → `SealConnectionSettings`;
     `loadDecryptedSettings` → `OpenConnectionSettings`; `identities.go:276` → `slack.BotToken`.
   - `usernotifications`: `SlackDMSender.SendDMTest` takes the resolved bot token instead of the
     row; `dispatchTestSlack` resolves it with `s.creds`. The interface seam stays.
   - `jobtypes/job_escalation_step.go`: `slack.BotToken(ctx, jctx.Services.Credentials, conn)`;
     a decrypt failure logs distinctly from a genuinely missing token; both still skip the route.
   - `opsnotifywire`: `sendSlackDM(dbSvc, registry)` so it can reach `registry.Credentials`.
   - `integrations/slack.Service`: `creds` field + `NewService` parameter (wired at
     `server.go:1949`); `GetClient`, `SetDefaultChannel` and `ListDestinations` resolve the token
     through the helper.
   - `jobtypes/job_notification.go` switches to `OpenConnectionSettings` (its own
     `ErrEncryptionDisabled` sentinel preserved by wrapping).
4. **Install write path** — `updateExistingChannel`, `createOrUpdateConnection` (update + create)
   and `SetDefaultChannel` seal through `SealConnectionSettings`, so `settings->>'access_token'`
   is NULL and `settings_private_keys` is `["access_token"]` from the first install onwards.
5. **`GetSlackChannelForOrg`** (postgres + sqlite): `ORDER BY is_default DESC, created_at ASC LIMIT 1`.
6. **Errors** — `ErrSlackClientNotConfigured` → "Slack app not installed for this organization".

### Tests
- `credentials`: `OpenConnectionSettings` over nil / plaintext-envelope / sealed / disabled-key;
  `ConnectionSecretFields(slack)` contains `access_token`; `SealConnectionSettings` round trip.
- `usernotifications`: split row (plaintext + AES-sealed) reaches the sender with the token;
  control = public-only row; negative = neither → `ErrSlackClientNotConfigured`.
- `jobtypes`: escalation Slack DM posts for a split row.
- `integrations/slack`: destinations + `GetClient` for a split row; token-less stub still
  `ErrSlackNotConnected`; install stores no public `access_token`.
- `db`: `GetSlackChannelForOrg` prefers the default integration (postgres + sqlite).
