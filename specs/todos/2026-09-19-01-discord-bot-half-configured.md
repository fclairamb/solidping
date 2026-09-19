---
model: opus
effort: high
---

# The Discord bot is advertised in production with half its credentials

*Reported by **Max** (`riskless.id`), an early user, on 2026-09-18, answering a
first-run feedback mail.*

## What Max said

> Overall a good platform. I'd like to have more customizability on what checks can be made.
> The Discord Bot doesnt work however. I get an oauth error. Did that get fixed?

(The customizability half is deliberately **not** in this spec — it is one
sentence of feedback with no specifics and a follow-up question has been sent.
Do not guess at it here.)

## What is actually wrong

`DiscordOAuthConfig` has six fields
([`config/discord_oauth.go`](server/internal/config/discord_oauth.go)):
`Enabled`, `ClientID`, `ClientSecret`, **`BotToken`**, `RedirectURL`, and
**`PublicKey`** — the last of which the file's own comment calls "mandatory for
the bot's buttons and slash commands — not an optional hardening".

**Production has three of them.** The `solidping-secrets` Secret in
`solidping-prod` carries exactly `SP_DISCORD_CLIENT_ID`,
`SP_DISCORD_CLIENT_SECRET` and `SP_DISCORD_ENABLED=true`. There is no
`SP_DISCORD_BOT_TOKEN` and no `SP_DISCORD_PUBLIC_KEY`. `solidping-dev` is
identical, which is why this has never been caught: **the bot has never been
fully configured in any deployed environment.**

The contrast with the neighbours in the same Secret is the tell — Slack has
`APP_ID`, `CLIENT_ID`, `CLIENT_SECRET`, `SIGNING_SECRET`, `ENABLED`; Telegram
has `BOT_TOKEN`, `BOT_USERNAME`, `WEBHOOK_SECRET`. Both are complete. Discord
has precisely the subset an **OAuth login provider** needs, and nothing the
**bot** needs. Someone provisioned "Discord" thinking of sign-in, and the bot
inherited the same flag.

### Why that reaches the user instead of failing closed

Two different gates, and only one of them is honest:

- Login is gated on all three values it needs —
  `Enabled && ClientID != "" && ClientSecret != ""`
  ([`providers_available.go:86`](server/internal/handlers/auth/providers_available.go#L86)).
  So "Sign in with Discord" is advertised and **works**; a user signed up
  through it on 2026-09-12.
- The bot integration is mounted on `Enabled && ClientID != ""`
  ([`server.go:914`](server/internal/app/server.go#L914)) — **it never checks
  `BotToken` or `PublicKey`**. So the install button, the routes and the OAuth
  callback are all live in production against an application that cannot
  complete an install.

`ErrBotTokenMissing` exists
([`bot_client.go:16`](server/internal/integrations/discord/bot_client.go#L16))
and is returned at call time, which is far too late: by then the user has been
bounced to Discord and back.

### The install URL asks for more than the app grants

`botInstallScopes = "bot applications.commands identify"`
([`service.go:87`](server/internal/integrations/discord/service.go#L87)), sent
with a `permissions` value to `https://discord.com/oauth2/authorize`.

The public application record disagrees. `GET
https://discord.com/api/v10/applications/<app-id>/rpc` (an unauthenticated
endpoint; the application id is public, it appears in every install URL) returns
for **both** the prod and dev applications:

```json
"install_params": { "scopes": ["applications.commands"], "permissions": "0" },
"flags": 0
```

So each app is configured for `applications.commands` with **zero** permissions,
while the code requests `bot` as well. That mismatch is the most likely source
of the literal "oauth error" Max saw, and it is console-side state no amount of
Go will fix.

## Findings from `failed_discord_auth.priv.har` (2026-08-24)

The capture was examined (it is gitignored and stays out of the repo; nothing
secret is reproduced here). **It does not contain a bot install at all.**

- Zero requests to `/integrations/discord` and zero `oauth2/authorize` requests
  carrying `scope=bot` or a `permissions` parameter. Both authorize requests in
  the file are the **login** provider round trip:
  `scope=identify email guilds`, `redirect_uri=https://solidping.io/api/v1/auth/discord/callback`.
- Discord itself did **not** fail. It answered the authorize POST with
  `{"location": "https://solidping.io/api/v1/auth/discord/callback?code=…&state=…"}`
  and `"authorized": true`. There is no `error` / `error_description` from
  discord.com anywhere in the capture.
- The failure is **SolidPing's own**. The capture's landing page is
  `https://solidping.io/api/v1/auth/discord/login?org=default&redirect_uri=/dash0/orgs/default?error=OAUTH_FAILED&error_description=OAuth+failed:+failed+to+find/create+organization:+sql:+no+rows+in+result+set`
  — i.e. the Discord **login** callback died on
  `failed to find/create organization: sql: no rows in result set`, and every
  subsequent dashboard call in the capture is a `401 NO_TOKEN`.
- The application record in the capture confirms the spec's console-state claim
  verbatim, for the prod app id `1500421248931336192`:
  `"install_params": {"scopes": ["applications.commands"], "permissions": "0"}`,
  `"flags": 0`, `bot_public: true`. It also carries a `verify_key` — which *is*
  the application's Ed25519 public key, so `SP_DISCORD_PUBLIC_KEY` can be read
  from the public `/applications/<id>/rpc` endpoint and is not a secret.

### What this does and does not establish

- **Established:** the prod Discord application grants only
  `applications.commands` with `permissions: "0"`, while
  [`botInstallScopes`](server/internal/integrations/discord/service.go#L87) asks
  for `bot applications.commands identify`. That mismatch is still the leading
  hypothesis for Max's "oauth error", and it remains console-side state.
- **Established:** a second, unrelated production bug — Discord **login** on
  `org=default` failed with `find/create organization: sql: no rows in result
  set` on 2026-08-24. Out of scope here; recorded so it is not lost.
- **NOT established:** the literal `error` / `error_description` Discord returns
  for the *bot install*. No capture of that round trip exists. Confirming it
  needs a human with a logged-in Discord session to click **Install Discord bot**
  in prod with the network panel open. §2 and §4 below are implemented so that
  the dead end cannot be reached at all, but they do not substitute for that
  capture.

## What this spec must produce

### 1. Reproduce it and capture the real error (do this first)

Nothing here has been confirmed against a live Discord account — the failure was
diagnosed from configuration and public application state, because the authorize
page needs a logged-in Discord session and the agent has none. **Do not skip
straight to the fix.** Click the install button in prod as a normal user, with
the network panel open, and record the exact `error` / `error_description`
Discord returns and which URL it lands on. Everything below is a hypothesis
until that string exists, and one such capture already sits unexamined in
`failed_discord_auth.priv.har` (2026-08-24) — which is its own lesson.

### 2. Fail closed instead of advertising a feature that cannot work

Split the one `Enabled` flag into what each half genuinely requires, the way
login already does:

- **Login** needs `Enabled + ClientID + ClientSecret`. Unchanged.
- **Bot** needs `Enabled + ClientID + ClientSecret + BotToken + PublicKey`.
  When those are not all present: do not mount the install routes
  ([`server.go:914`](server/internal/app/server.go#L914)), do not render the
  install button, and log one clear WARN at boot naming the missing keys.

A user must never be able to start an OAuth round trip the deployment cannot
finish. This is the part that would have turned Max's dead end into a channel
that simply was not offered.

### 3. Provision the missing credentials (ops, not code)

Both environments need, from the Discord developer portal:

| Value | Env var | gopass |
|---|---|---|
| Bot token | `SP_DISCORD_BOT_TOKEN` | `k8xp/solidping-prod/discord/bot-token` |
| App public key (Ed25519, hex) | `SP_DISCORD_PUBLIC_KEY` | `k8xp/solidping-prod/discord/public-key` |

…and the application itself needs its install params to include the `bot` scope
with the permissions the code asks for, plus
`https://solidping.io/api/v1/integrations/discord/oauth` in its redirect URIs
(`SP_BASE_URL` is `https://solidping.io`, and the callback path is built by
[`installRedirectURI()`](server/internal/integrations/discord/service.go#L263)).

**This is console work and it is a human's**: reading a bot token out of a web
form is exactly the case where the value must go into gopass by hand, never
through an agent's context. The same applies to dev.

### 4. A test that would have caught it

The existing e2e ([`channels-discord-bot.spec.ts`](web/dash0/e2e/channels-discord-bot.spec.ts))
passes against a configuration no deployment has. Add a boot-level assertion
instead: with `Enabled=true`, `ClientID`/`ClientSecret` set and `BotToken` empty,
the install route must **not** be registered. That is the exact production
state, and today it mounts.

## Why this is worth doing properly

Max is one of eight real users. He wired Discord, hit a wall, and still called
solidping "a good platform" — he went out of his way to report it rather than
leave. A notification channel that is offered and then fails at the OAuth step
is worse than one that is absent: it costs the user their time and it is the
first impression of the product's reliability, from a monitoring tool.
