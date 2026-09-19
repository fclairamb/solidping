# Runbook — provisioning the Discord bot

The Discord **bot** and Discord **login** are two different features that share
one application record and one `SP_DISCORD_ENABLED` flag, and they need
different credentials. Every deployed environment has run with login's
credentials and none of the bot's since the bot shipped, which is how
production came to advertise an install that could not complete
(spec `2026-09-19-01`).

Since that spec, the bot **fails closed**: with anything missing, the install
routes are not mounted, the dashboard renders no install button, and the API
logs one WARN at boot naming the missing keys. This runbook is how you make it
open.

| Feature | Needs |
|---|---|
| Sign in with Discord | `SP_DISCORD_ENABLED`, `SP_DISCORD_CLIENT_ID`, `SP_DISCORD_CLIENT_SECRET` |
| Discord bot (install, threads, Acknowledge button, slash commands) | all of the above **plus** `SP_DISCORD_BOT_TOKEN` and `SP_DISCORD_PUBLIC_KEY` |

The predicate is `config.DiscordOAuthConfig.BotConfigured()`
(`server/internal/config/discord_oauth.go`) — one definition, read by the route
table, by the minted install URL, and by the `discord.botEnabled` flag in
`GET /api/v1/config` that the dashboard renders the button on.

## The boot warning

```
WARN Discord bot disabled: missing configuration
     missing="SP_DISCORD_BOT_TOKEN, SP_DISCORD_PUBLIC_KEY"
     effect="install routes not mounted, install button hidden; Discord login unaffected"
```

That is the whole diagnosis. If you see it, work through this file. If Discord
is off entirely (`SP_DISCORD_ENABLED` unset), nothing is logged — an operator
who never wanted Discord is not missing anything.

## 1. Read the two values out of the developer portal

<https://discord.com/developers/applications> → the SolidPing application.

| Value | Where | Notes |
|---|---|---|
| **Bot token** | *Bot* → *Token* → **Reset Token** | Shown **once**. Resetting invalidates the previous token, so every environment using it must be updated in the same pass. A real secret. |
| **Public key** | *General Information* → *Public Key* (Ed25519, hex) | **Not** a secret — it is served publicly as `verify_key` by `GET https://discord.com/api/v10/applications/<app-id>/rpc`. Stored alongside the token purely for convenience. |

**Read these by hand and put them straight into gopass.** A bot token copied
out of a web form must not travel through an agent's context, a chat message,
a shell history line or a tracked file. If one does, reset it in the portal and
start again.

```bash
gopass insert k8xp/solidping-prod/discord/bot-token
gopass insert k8xp/solidping-prod/discord/public-key
gopass insert k8xp/solidping-dev/discord/bot-token
gopass insert k8xp/solidping-dev/discord/public-key
```

Dev and prod should be **separate Discord applications** with separate tokens,
for the same reason Telegram needed two bots: one application record holds one
interactions endpoint URL, so sharing it means whichever environment was
configured last receives everything.

## 2. Fix the application record itself

The application's own install parameters are the other half of the failure, and
no amount of Go fixes them. As captured on 2026-08-24, both the dev and prod
applications answer:

```json
"install_params": { "scopes": ["applications.commands"], "permissions": "0" }
```

while the code asks for `bot applications.commands identify` with a real
permission integer (`botInstallScopes` / `botPermissions`,
`server/internal/integrations/discord/service.go`). That mismatch is the
leading suspect for the literal "oauth error" an early user reported. In the
portal:

- *Installation* → **Guild Install** scopes: `bot`, `applications.commands`,
  `identify`.
- *Installation* → default permissions: the set `botPermissions` composes
  (View Channel, Send Messages, Embed Links, …) — read the constant, do not
  guess an integer.
- *OAuth2* → **Redirects**: add
  `https://solidping.io/api/v1/integrations/discord/oauth`
  (dev: the same path under the dev base URL). This is what
  `installRedirectURI()` builds from `SP_BASE_URL`; a redirect URI that is not
  registered is itself an `invalid_request` at the authorize step.
- *General Information* → **Interactions Endpoint URL**:
  `https://solidping.io/api/v1/integrations/discord/interactions`. Discord
  probes it with a signed ping and **deactivates** it if verification fails —
  which is exactly what happens when `SP_DISCORD_PUBLIC_KEY` is unset, so set
  the env var **before** saving this field.

## 3. Deploy the values

They go into the `solidping-secrets` Secret of each namespace, next to the
existing `SP_DISCORD_CLIENT_ID` / `SP_DISCORD_CLIENT_SECRET` /
`SP_DISCORD_ENABLED`. Both values can equally be set as system parameters
(`internal/systemconfig` reads `discord.bot_token` and `discord.public_key`),
which is the self-hosted path.

## 4. Verify

```bash
# The flag the dashboard reads. Must be true.
curl -s https://solidping.io/api/v1/config | jq .discord

# The install route must now exist (anything but 404/405).
curl -s -o /dev/null -w '%{http_code}\n' \
  https://solidping.io/api/v1/integrations/discord/oauth
```

Then, in the dashboard, open a Discord channel's settings: **Install Discord
bot** must be offered, and clicking it must land on a discord.com authorize
page that names the SolidPing application and the guild picker. If it instead
returns an `error` / `error_description`, capture that string — it is still the
one piece of evidence this investigation never had.

## Related

- Spec: `specs/todos/2026-09-19-01-discord-bot-half-configured.md` (or its
  archived path under `specs/done/`).
- `wiki/discord/README.md` — what the bot does once it is installed.
