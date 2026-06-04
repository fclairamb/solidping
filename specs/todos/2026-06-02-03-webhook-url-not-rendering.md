# Webhook URL not rendering on the integration edit page

## Context

The integration edit page `…/integrations/<uid>` renders the **Webhook URL field blank**
when editing an existing webhook integration. Reported on
`http://localhost:4000/dash0/orgs/default/integrations/d9d0e616-a7fb-49d3-aff3-71ed99f804b3`
(org `default`).

### Root cause — confirmed three ways (code read, secret-field registry, live API call)

The webhook URL is stored under the `url` key and is listed as a **secret** field in
[`server/internal/crypto/credentials/conn_secrets.go:28`](../../server/internal/crypto/credentials/conn_secrets.go):

```go
models.ConnectionTypeWebhook: {"url", "auth_token", "signingSecret", "signingSecretPrevious"},
```

On read, `toResponse()`
([`server/internal/handlers/integrations/service.go:184-219`](../../server/internal/handlers/integrations/service.go))
strips every key in the secret-field set from the response `settings` (loop at lines 200-209).
For webhook integrations, `GetIntegration()` then calls `injectSigningSecrets()` (`service.go:300-335`),
which re-injects **only** `signingSecret` / `signingSecretPrevious` back into `settings` — the
`url` is never re-injected. The frontend `UrlPanel` reads `settings.url` directly
([`web/dash0/src/components/integrations/integration-form.tsx:234`](../../web/dash0/src/components/integrations/integration-form.tsx)),
so it receives `undefined` → empty input.

Live `GET /api/v1/orgs/default/integrations/<uid>` confirms it:

```json
{ "type": "webhook", "settings": { "signingSecret": "whsec_…" } }   // no "url"
```

### Same bug, sibling types

`discord`, `googlechat`, and `mattermost` store their URL under `webhook_url`, which is **also**
in their secret list (`conn_secrets.go:27,30,31`), and they have **no** injection path at all —
so their edit forms render the webhook URL blank too (`integration-form.tsx:252`).

### Why the dev record is fixable without a migration

The inspected dev server runs with encryption **disabled** (no `SP_ENCRYPTION_MASTER_KEY`). In
that plaintext-fallback mode the `url` lives in the public `settings` JSONB in plaintext and is
*only* being hidden by the on-read strip. Removing `url` from the secret list makes it flow
straight through. The data backfill below is needed only for deployments that had encryption
**enabled** when the integration was saved (there the `url` sits in the encrypted
`settings_private` blob).

### Decisions

- **Approach:** make the URL a **non-secret (plaintext)** field — remove it from the secret
  registry rather than injecting it on read. Accepted tradeoff: webhook URLs are no longer
  encrypted at rest; the threat model in `server/CLAUDE.md` is DB-theft only.
- **Scope:** fix **all** URL-bearing connection types — webhook `url` **and**
  discord / googlechat / mattermost `webhook_url`.

## Goals

- Editing any existing webhook / discord / googlechat / mattermost integration shows the saved
  URL in the form.
- New writes keep the URL in plaintext public `settings`.
- Existing encrypted records are backfilled so their URL moves from the encrypted blob to public
  plaintext.
- Signing-secret and auth-token behavior is unchanged.

## Implementation

### 1. Registry change — the core fix

In `server/internal/crypto/credentials/conn_secrets.go` (`connectionSecretFields`, lines 25-36):

- `ConnectionTypeWebhook`: drop `"url"` → keep `{"auth_token", "signingSecret", "signingSecretPrevious"}`.
- `ConnectionTypeDiscord` / `ConnectionTypeGoogleChat` / `ConnectionTypeMattermost`: drop
  `connKeyWebhookURL`. These then have no secret keys — leave the map entry empty or remove it.
  The `connKeyWebhookURL` constant may become unused; remove it (and any now-dead reference) to
  satisfy the `goconst`/unused linters, or keep it only if still referenced elsewhere.

This immediately fixes:

- all plaintext-fallback records (URL already in public `settings`, just stops being stripped), and
- all future writes — `applySettingsEncryption` (`service.go:489-530`) re-splits on the current
  registry, so the URL stays in public `settings`.

**No change** is needed in `toResponse`, `GetIntegration`, `injectSigningSecrets`, or the
frontend — `integration-form.tsx` already reads `settings.url` / `settings.webhook_url`, and
`injectSigningSecrets` keeps handling the still-secret `signingSecret` / `signingSecretPrevious`.

### 2. Data backfill — for encryption-enabled deployments only

Existing records written with encryption on have the URL in the encrypted `settings_private`
blob, with the key recorded in `settings_private_keys`. The existing `credmigrate.Run()`
([`server/internal/credmigrate/credmigrate.go`](../../server/internal/credmigrate/credmigrate.go))
is **forward-only** (plaintext→encrypted) and **skips rows whose private column is already set**,
so it will *not* demote a field — a new backfill is required.

Add a one-shot, idempotent "reconcile to current registry" pass for `integration_connections`
that, for each affected row:

1. reconstitutes the full plaintext = `MergeConfig(public settings, DecryptForOrg(settings_private))`
   — reuse the same logic as `loadDecryptedSettings` (`service.go:535-566`);
2. re-splits via `SplitConfig(full, ConnectionSecretFields(type))` against the **new** registry
   (`secret_fields.go`), moving `url` / `webhook_url` from private → public;
3. re-encrypts the remaining secrets with `EncryptForOrg` (or nulls `settings_private` /
   `settings_private_keys` when none remain) and updates the row.

Details:

- **Idempotency:** skip rows where `settings_private_keys` no longer contains the URL key, so
  re-running is a no-op.
- **Reuse:** `SplitConfig` / `MergeConfig` / `EncryptForOrg` / `DecryptForOrg` from
  `server/internal/crypto/credentials/`.
- **Wiring:** mirror `credmigrate`'s invocation points — the startup auto-migrate hook
  (`server/internal/app/server.go:1772-1799`, `MaybeAutoMigrateEncryption`) and the
  `encrypt-credentials` CLI (`server/main.go:235-291`), or a sibling CLI command.
- **No schema/SQL migration** and no SQLite/Postgres divergence: it operates on existing columns
  through the `db.Service` abstraction.
- **No-op when encryption is disabled** — those records are already fixed by step 1.

### 3. Tests

- `server/internal/handlers/integrations/service_test.go`: `GetIntegration` returns the URL in
  `settings` for `webhook` (`url`) and for `discord` / `googlechat` / `mattermost` (`webhook_url`);
  cover both encryption-on and encryption-off; assert `signingSecret` / `auth_token` behavior is
  unchanged (signing secret still surfaced, `auth_token` still masked).
- `server/internal/crypto/credentials/` (e.g. `conn_secrets_test.go` if present): assert `url` /
  `webhook_url` are no longer reported by `ConnectionSecretFields` for those types.
- `server/internal/credmigrate/`: backfill test — seed an encrypted webhook record with `url` in
  the private blob, run the backfill, assert `url` lands in public `settings`, is removed from
  `settings_private_keys`, remaining secrets stay encrypted, and re-running is a no-op.

## Verification

1. **Reported record (encryption off):** restart the dev server, then
   `GET /api/v1/orgs/default/integrations/d9d0e616-a7fb-49d3-aff3-71ed99f804b3` →
   `settings.url` is present and correct.
2. **UI:** open `…/integrations/d9d0e616-a7fb-49d3-aff3-71ed99f804b3`, confirm the Webhook URL
   field is populated; repeat for a discord / googlechat / mattermost integration. Prefer a dash0
   Playwright e2e (`web/dash0/e2e/`) over manual checks.
3. **Round-trip:** edit + save the integration, reload, confirm the URL persists and the signing
   secret / auth-token fields are unaffected.
4. **Backfill (encryption on):** Go test seeds an encrypted record, runs the backfill, asserts
   the URL moves to public plaintext and the row is left consistent.
5. `make test` and `make lint` pass.

## Files referenced

- `server/internal/crypto/credentials/conn_secrets.go` — registry change (primary).
- `server/internal/crypto/credentials/secret_fields.go`, `service.go` — reused split/merge/crypto helpers.
- `server/internal/handlers/integrations/service.go` — `toResponse` / `GetIntegration` /
  `injectSigningSecrets` / `loadDecryptedSettings` / `applySettingsEncryption` (read context; tests).
- `server/internal/credmigrate/credmigrate.go` — pattern + invocation points for the backfill.
- `server/internal/app/server.go:1772-1799`, `server/main.go:235-291` — backfill wiring.
- `web/dash0/src/components/integrations/integration-form.tsx` — confirms no frontend change needed.
