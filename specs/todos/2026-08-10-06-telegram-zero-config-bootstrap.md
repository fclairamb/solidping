---
model: opus
effort: high
---

# Telegram needs only a bot token — derive the rest, and fix webhook re-registration

## Problem

Two problems with one root cause: the Telegram integration
(`specs/done/2026/08/2026-08-10-04-telegram-alert-channel.md`) asks the operator
for values the server can determine itself, and one of those values can silently
diverge from what Telegram holds with no way to detect it.

### 1. Four env vars are demanded; only one is irreducible

[.env.example:26](.env.example#L26) tells operators the feature is "entirely
inert unless `SP_TELEGRAM_ENABLED`, `SP_TELEGRAM_BOT_TOKEN` and
`SP_TELEGRAM_BOT_USERNAME` are all set". Only the token is genuinely required —
it *is* the bot's identity. The rest:

- **`SP_TELEGRAM_BOT_USERNAME`** is already fetched at boot.
  `verifyTelegramIdentity`
  ([telegram_bootstrap.go:60](server/internal/app/telegram_bootstrap.go#L60))
  calls `GetMe`, reads `bot.Username`, uses it only to warn about a mismatch,
  and throws it away. The server knows the answer and asks anyway.
- **`SP_TELEGRAM_WEBHOOK_SECRET`** is a value *we* choose and hand to Telegram
  in the same `setWebhook` call the server already makes. No third party ever
  needs to know it — unlike WhatsApp's verify token, which Meta requires a human
  to paste into its dashboard. That asymmetry is why copying the WhatsApp config
  shape here was the wrong instinct. When it is empty, `ensureTelegramWebhook`
  refuses to register and logs "the webhook will reject every update until one
  is set": a fully-configured-looking instance that quietly does nothing.
  And because it is the **only** authenticity gate on a public endpoint
  (Telegram does not sign payloads —
  [telegramcb/handler.go:8](server/internal/handlers/telegramcb/handler.go#L8)),
  leaving it to operator choice means accepting `secret123` on the sole lock.
- **`SP_TELEGRAM_ENABLED`** is ceremony: the presence of a bot token *is* the
  intent to enable.

### 2. The webhook is never re-registered when only the secret changes

`ensureTelegramWebhook`
([telegram_bootstrap.go:113](server/internal/app/telegram_bootstrap.go#L113))
returns early when the registered URL already matches:

```go
if info.URL == want { /* … last-error diagnostics … */ return }   // SetWebhook never called
```

But `getWebhookInfo` **never returns the secret**. `WebhookInfo`
([client.go:378](server/internal/integrations/telegram/client.go#L378)) carries
only `url`, `pending_update_count` and `last_error_message`, and that is all the
Bot API exposes — `secret_token` is write-only.

So a secret that changes at a constant URL is never pushed to Telegram.
Telegram keeps sending the old `X-Telegram-Bot-Api-Secret-Token`, the handler
compares it to the new one ([handler.go:116](server/internal/handlers/telegramcb/handler.go#L116))
and **403s every update**: the connect flow, in-chat `/stop`, and block
notifications all die silently. The only trace is `last_error_message`, surfaced
at the *next* boot. This is a defect in shipped code today; auto-generating the
secret would turn it from a rotation footgun into a routine failure.

## Proposal

### Phase 1 — Fix re-registration (standalone, land it first)

Call `SetWebhook` **unconditionally** whenever Telegram is configured and a base
URL exists. It is idempotent and costs one API call per boot, which buys
convergence by construction for a value that cannot be read back.

Keep `GetWebhookInfo` for the `last_error_message` / `pending_update_count`
diagnostic — it is genuinely useful — but it must no longer gate registration.

> Considered and rejected: persisting a fingerprint of the registered secret and
> re-registering on mismatch. That is more state, another thing to get out of
> sync, and no better than one idempotent call.

This phase depends on nothing else in this spec and fixes a live bug. Ship it
first, with its regression test.

### Phase 2 — Split `Active()` into two questions

`Active()` ([config.go:822](server/internal/config/config.go#L822)) is currently
`Enabled && BotToken != "" && BotUsername != ""`, conflating *can we send* with
*can a user connect*. That conflation is precisely why the username became
mandatory.

| Predicate | Rule | Gates |
|---|---|---|
| `Configured()` | `Enabled && BotToken != ""` | webhook route ([server.go:1446](server/internal/app/server.go#L1446)), escalation dispatch ([job_escalation_step_telegram.go:68](server/internal/jobs/jobtypes/job_escalation_step_telegram.go#L68)), boot-time bootstrap |
| `Active()` | `Configured() && BotUsername != ""` | connect surface only: public config flag ([publicconfig/handler.go:108](server/internal/handlers/publicconfig/handler.go#L108)) and link endpoint ([usernotifications/telegram.go:111](server/internal/handlers/usernotifications/telegram.go#L111)) |

**The webhook route must not depend on the username.** If it does, a first boot
holding only a token registers no route, and no amount of `GetMe` can fix that
without a restart. Registering it early is harmless: the handler rejects
everything that fails the secret check, so an unconfigured deployment exposes a
403-only endpoint.

### Phase 3 — Derive and persist

Two system parameters, via the existing
`SetSystemParameter(ctx, key, value, secret)`
([db/service.go:560](server/internal/db/service.go#L560)):

| Key | Secret | Source when absent |
|---|---|---|
| `telegram.bot_username` | no | `GetMe` |
| `telegram.webhook_secret` | **yes** | `crypto/rand`, 32 bytes, base64url |

Resolution order for both: **env wins**, else the persisted parameter, else
derive/generate and persist. Env keeps priority so an operator fronting the
webhook with a proxy that must know the secret — or running declarative/GitOps
config — is never fought by the server.

Two hard requirements:

- **Atomic get-or-create.** Only `GetSystemParameter`/`SetSystemParameter`
  exist; there is no equivalent of `GetOrCreateStateEntry`. With several API
  pods booting together, each would generate its own secret, the last writer
  would win, and the losers would validate against a secret Telegram no longer
  holds → 403 on everything. Add
  `GetOrCreateSystemParameter(ctx, key, value any, secret bool) (*models.Parameter, bool, error)`
  to `db.Service` (postgres + sqlite), as a single atomic insert-if-absent
  returning whether it created.
- **Persist before registering.** The reverse order can leave Telegram holding a
  secret no pod in the fleet knows.

### Phase 4 — Where resolution runs, and the data race to avoid

`cfg` is read concurrently by request handlers, and nothing mutates it after
startup today. Hydrating it from the existing
`go bootstrapTelegram(runnerCtx, s.config)` goroutine
([server.go:2392](server/internal/app/server.go#L2392)) would introduce a
genuine data race that `-race` will flag. So resolution runs **synchronously,
before route registration**:

- DB read + secret generation: local, fast, unconditional.
- `GetMe`: only when the username is unknown from *both* env and DB — i.e. the
  very first boot. Bounded by a short timeout (3s) so an unreachable Telegram
  delays startup by at most that, once, and never fails it.

After the first boot the username is cached in the DB, so the startup path makes
no network call at all. The existing async `bootstrapTelegram` keeps the
`setWebhook` + diagnostics half.

First boot with only a token has a sub-second window where `/api/v1/config`
reports the connect surface off; a dashboard loaded in that window hides the
connect button until refresh. Acceptable, and it self-heals without a restart.

### Phase 5 — `Enabled` becomes tri-state

`Enabled *bool`: `nil` = auto (on iff a bot token is present), `false` = kill
switch, `true` = explicit on. A bare `bool` cannot express "unset", which is
what makes the token alone sufficient.

`enabled` is a single-word koanf key read by the env provider, so the manual
`applyTelegramEnv` reader
([config.go:1958](server/internal/config/config.go#L1958)) is unaffected —
but `Defaults()` and every `TelegramConfig` struct literal (including
[envvars_test.go:199](server/internal/config/envvars_test.go#L199)) need
updating.

### Phase 6 — Docs and env

- [.env.example:26](.env.example#L26): replace the "all three must be set"
  sentence with `SP_TELEGRAM_BOT_TOKEN` alone being sufficient, everything else
  an optional override.
- `web/docs/docs/configuration/telegram.md`: a required-vs-optional table, what
  the server derives and where it persists it, and rotation procedures — revoke
  in @BotFather + restart for the token; delete the `telegram.webhook_secret`
  parameter + restart for the secret (which now re-registers correctly thanks to
  Phase 1).
- While `/status`, `/ack` and `/mute` remain unimplemented, the documented
  @BotFather command list must advertise only `start` and `stop`.

### Tests

- **Re-registration**: `SetWebhook` is called even when the registered URL
  already matches, and a changed secret reaches Telegram. Write this one first
  and watch it fail against current code — it is the regression test for the
  shipped bug.
- **Get-or-create**: two concurrent callers yield one value; the second sees
  `created == false`.
- **Precedence**: env > parameter > derived, for both keys — including "env set
  *and* a different parameter present" → env wins and the stored parameter is
  left untouched.
- **Generated secret**: ≥32 bytes of entropy, URL-safe, distinct across
  instances, never logged and never present in `/api/v1/config`.
- **`Configured()` vs `Active()`**: a token-only config registers the webhook
  route and dispatches escalations while the public config still reports the
  connect surface off.
- **First boot with only a token**: username fetched, persisted, connect link
  correct; a second boot makes no `GetMe` call.
- **`GetMe` unreachable on first boot**: startup completes within the timeout,
  connect stays off, nothing crashes, and the next boot recovers.
- `-race` over the startup path, given Phase 4 exists to prevent exactly that.

### Out of scope

- Rotating the bot token itself — an operator action in @BotFather.
- An admin UI for these parameters; the system-parameter API suffices.
- Long-polling mode: still the known gap carried over from
  `2026-08-10-04-telegram-alert-channel.md`.
- Per-org / multi-bot Telegram.

## Implementation Plan

Follows the spec's own phasing. Phase 1 lands first, alone, with its regression
test.

### Phase 1 — unconditional `SetWebhook` (standalone, first commit)

- `server/internal/app/telegram_bootstrap.go`: drop the
  `if info.URL == want { … return }` early return. `GetWebhookInfo` stays, but
  only to feed the `lastError` / `pendingUpdates` / `previous URL` diagnostics —
  a failure to read it no longer aborts registration either.
- New `server/internal/app/telegram_bootstrap_test.go` with an httptest fake Bot
  API that records every method call. Regression test: with `getWebhookInfo`
  already returning our exact URL, `setWebhook` **is** called, and the payload
  carries the *current* secret.

### Phase 2 — `Configured()` vs `Active()`

- `config.TelegramConfig`:
  - `Configured() = IsEnabled() && BotToken != ""`
  - `Active()    = Configured() && BotUsername != ""`
- Move to `Configured()`: webhook route registration (`app/server.go`),
  escalation dispatch (`jobs/jobtypes/job_escalation_step_telegram.go`),
  `bootstrapTelegram`, and `telegram.NewClientFromConfig` (sending never needed
  the username).
- Keep `Active()`: public config flag (`handlers/publicconfig`), connect-link
  endpoint (`handlers/usernotifications/telegram.go`).

### Phase 3 — `GetOrCreateSystemParameter` on both backends

- `db.Service` interface gains
  `GetOrCreateSystemParameter(ctx, key string, value any, secret bool) (*models.Parameter, bool, error)`.
- Postgres + SQLite: read-then-`INSERT … ON CONFLICT (key) WHERE deleted_at IS
  NULL AND organization_uid IS NULL DO NOTHING`, re-reading on a lost race —
  the `GetOrCreateStateEntry` pattern, against the existing partial unique index
  `parameters_system_key_idx`. Same code shape on both engines
  (`sync-pg-to-sqlite`).
- Cross-engine test in the shared `testService` suite: create → `created==true`;
  second call with a different value → `created==false`, same UID, original
  value preserved.

### Phase 4 — synchronous resolution before route registration

New `server/internal/app/telegram_resolve.go`:

- Parameter keys `telegram.bot_username` (public) and `telegram.webhook_secret`
  (secret), matching the `parameters` key convention.
- `resolveTelegramSettings(ctx, dbSvc, cfg)`, called at the **top of
  `SetupRoutes`** — synchronous, so `cfg.Telegram` is fully hydrated before any
  handler can read it and no goroutine ever writes it (the `-race` requirement
  of Phase 4). No-op unless `cfg.Telegram.Configured()`.
- Webhook secret: env/YAML value wins untouched; otherwise
  `GetOrCreateSystemParameter` with a freshly generated 32-byte
  base64url (unpadded) candidate, and the **returned** value is used — so a pod
  that loses the race adopts the winner's secret.
- Bot username: env/YAML wins; else the stored parameter; else `GetMe` bounded
  by a 3 s timeout, persisted with `GetOrCreateSystemParameter`. Failure is a
  warning, never fatal — the connect surface simply stays off until the next
  boot.
- Persist happens here, strictly before the async `bootstrapTelegram`
  registers the webhook.
- `verifyTelegramIdentity` no longer warns about a mismatch when no username was
  configured (it is now derived, so there is nothing to disagree with).

### Phase 5 — tri-state `Enabled`

- `TelegramConfig.Enabled` becomes `*bool`; `IsEnabled()` returns `*Enabled`
  when set, else `BotToken != ""`.
- `Defaults()` leaves it `nil` (auto). Update `envvars_test.go` literals and the
  `Enabled` assertion; add cases for nil/true/false.

### Phase 6 — docs and env

- `.env.example`: `SP_TELEGRAM_BOT_TOKEN` alone is sufficient; everything else
  optional; `SP_TELEGRAM_ENABLED` documented as unset=auto / false=kill switch.
- `web/docs/docs/configuration/telegram.md`: required-vs-optional table, what
  the server derives and where it persists it, both rotation procedures, and a
  re-check that the @BotFather command list advertises only `start`/`stop`.

### Tests (mapped to the spec's list)

| Spec bullet | Where |
|---|---|
| Re-registration | `app/telegram_bootstrap_test.go` |
| Get-or-create, concurrent | `db/service_test.go` (both engines) |
| Precedence env > param > derived | `app/telegram_resolve_test.go` |
| Generated secret entropy / URL-safe / distinct / never in `/api/v1/config` | `app/telegram_resolve_test.go` + `publicconfig` test |
| `Configured()` vs `Active()` | `config/envvars_test.go` + route/dispatch tests |
| First boot only a token; second boot no `GetMe` | `app/telegram_resolve_test.go` |
| `GetMe` unreachable on first boot | `app/telegram_resolve_test.go` |
| `-race` over the startup path | `go test -race ./internal/app/... ./internal/config/...` |
