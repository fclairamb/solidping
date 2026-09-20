---
model: opus
effort: high
---

# The Discord bot alerts a channel like Slack does, but cannot page a person, mention an unmapped member, or prove any of it in E2E

## Problem

Spec `2026-08-21-06-discord-bot-integration` brought the Discord bot to Slack
parity for the **org** integration: install, channel picker, embeds with real
threads, Acknowledge / Unavailable / Escalate buttons, on-call mentions, comment
ingestion, slash and mention commands, Gateway supervisor. Its resolved question
4 deliberately left one thing out — *"Per-user Discord DM contact — out of
scope … a cheap follow-up once the bot exists, and belongs in its own spec."*
This is that spec, plus the two smaller gaps that fell out of the same decision.

### 1. A person cannot be paged on Discord

`user_contacts` knows email, phone, `slack_user`, Pushover, ntfy, web push,
WhatsApp and Telegram
([user_contact.go:10-35](../../server/internal/db/models/user_contact.go)) —
no Discord. Every per-person delivery path therefore skips Discord:

- escalation steps that target a person —
  [job_escalation_step.go:521-560](../../server/internal/jobs/jobtypes/job_escalation_step.go)
  `dispatchRoute`, with `sendEscalationSlackDM` at `:569` as the Slack
  counterpart;
- operator notices —
  [opsnotify/deliver.go:292-310](../../server/internal/opsnotify/deliver.go)
  `dispatchRoute`;
- the account page's Test button —
  [usernotifications/service.go:619](../../server/internal/handlers/usernotifications/service.go)
  `dispatchTestRoute`;
- the support inbox: a DM the Gateway already captures
  ([gateway_messages.go:234](../../server/internal/integrations/discord/gateway_messages.go)
  `captureDirectMessage`) can never be attributed to a member, because
  `contactTypeForChannel` in
  [support/service.go](../../server/internal/support/service.go) has no Discord
  vocabulary (its own comment says so).

The bot client has no way to open a DM either:
[bot_client.go](../../server/internal/integrations/discord/bot_client.go)
exposes `CreateMessage`, `EditMessage`, `StartThreadFromMessage`,
`UnarchiveThread`, `ListGuildChannels`, `GetGuild`, `GetCurrentUser`,
`GetChannel` — nothing for `POST /users/@me/channels`.

### 2. A member who never signed in with Discord cannot be mentioned, and cannot fix it

On-call mentions for Discord resolve from exactly one source, the admin's
`user_integration_identities` row
([mentions.go:272-320](../../server/internal/jobs/jobtypes/mentions.go)
`buildMentionTargets`). The fallback added for Slack in
`2026-09-19-02-slack-on-call-mention-declared-handle` —
`identitylink.DeclaredSlackIdentity`, consulted at `mentions.go:301`,
[identities.go:425](../../server/internal/handlers/integrations/identities.go)
and
[usernotifications/service.go:326](../../server/internal/handlers/usernotifications/service.go)
— is Slack-only; `server/internal/identitylink/` contains `slack.go` and
nothing else.

The admin side cannot compensate: Discord has no look-up-by-email, so
`lookupDiscordIdentities` (`identities.go:445`) matches only members with a
Discord sign-in on file, and the dashboard's mapping panel deliberately renders
no picker for the `discord` variant
([integration-form.tsx:1885-1895](../../web/dash0/src/components/integrations/integration-form.tsx)).
And the member side has no affordance at all:
[account.notifications.tsx](../../web/dash0/src/routes/orgs/$org/account.notifications.tsx)
has a `SlackConnectRow` (`:553`) and a `TelegramConnect` button (`:223`), no
Discord row, and no "you will be pinged as …" line for Discord.

### 3. The destinations picker has no DM tab

`DiscordDestinationsResponse` carries `Channels` only
([discord/service.go:670](../../server/internal/integrations/discord/service.go));
`SlackDestinationsResponse` carries `Channels` and `Users`
([slack/service.go:1301](../../server/internal/integrations/slack/service.go)).
A check cannot route its alerts to one person's Discord DMs the way it can on
Slack.

### 4. Nothing above the install flow is covered end to end

| | Slack | Discord |
|---|---|---|
| dash0 E2E files | 5 (`channels-slack-install`, `slack-member-mapping`, `slack-comment-ingestion`, `slack-socket-mode`, `account-notifications-slack-mention`) | 1 (`channels-discord-bot`) |
| dash0 E2E tests | 19 | 4 |
| Go `_test.go` files in the integration package | 24 | 6 |

Member mapping, comment ingestion, the instance-level Gateway status page
(`server.discord.tsx` exists, `slack-socket-mode.spec.ts` has no counterpart)
and the account page are untested for Discord. Given how many Slack readers
silently rotted around the token move (`2026-09-18-02`), that is where the next
Discord regression will hide.

## Proposal

Sections 1–3 are the feature; section 4 is the DM tab; section 5 is the test
parity. All of it is in scope. If anything must be cut, it is section 4, and
that cut needs the maintainer's explicit OK, not an implementer's judgement.

### 1. `discord` user contact, delivered by the instance bot

- `UserContactTypeDiscord = "discord"` in `user_contact.go`. `Value` is the
  Discord **user id** (snowflake). The DM channel id that
  `POST /users/@me/channels` returns is cached on the contact so paging does not
  re-open a DM on every notice — a nullable column added in migration
  `023`, both dialects, exactly the way `022_v0_30_0` added `team_id`.
  `contactRequiresSetup` (`usernotifications/service.go:610`) treats it like
  Telegram: connected or not, no verification code.
- `BotClient.CreateDM(ctx, userID)` → `POST /users/@me/channels
  {"recipient_id": …}`. It is the **instance** bot (`SP_DISCORD_BOT_TOKEN`),
  the way the Telegram path uses the instance Telegram bot, so a DM contact
  needs no org integration — only `config.Discord.BotConfigured()` (from
  `2026-09-19-01`). The account page offers the contact type only when
  `publicconfig` says `discord.botEnabled`
  ([public-config.ts:81](../../web/dash0/src/api/public-config.ts)
  `useDiscordBotEnabled`).
- Delivery, mirrored from the Slack DM / Telegram cases:
  - `opsnotify/deliver.go` `dispatchRoute` gains `case UserContactTypeDiscord`
    → `sendDiscordDM`. Discord error `50007` ("Cannot send messages to this
    user": DMs closed, or no shared guild) maps to `skipUnavailable`
    (`deliver.go:68`) so paging coverage falls through to the member's next
    route instead of counting as delivered.
  - `job_escalation_step.go` `dispatchRoute` gains the same case →
    `sendEscalationDiscordDM`, mirroring `sendEscalationSlackDM` (`:569`): the
    incident embed **with** the `discord.IncidentActionRow` button row
    (`notifications/discord.go:430`), so Acknowledge from a DM goes through the
    existing interactions endpoint unchanged. Add `severityAllowsDiscord` next
    to `severityAllowsTelegram` (`:410`).
  - `usernotifications/service.go` `dispatchTestRoute` gains
    `dispatchTestDiscord`, mirroring `dispatchTestTelegram` (`:660`). A `50007`
    on Test is shown to the member as "Discord refused the DM — open your DMs
    for server members or join the server the bot is in", not as a generic
    failure.
  - `support/service.go` `contactTypeForChannel` maps `SupportChannelDiscord`
    → `UserContactTypeDiscord`, so captured DMs attribute to the member.
- `member-coverage.tsx` and the paging-coverage computation count a Discord
  contact as a real route, with the same "unavailable" degradation as Telegram.

### 2. Connecting Discord is a verified binding, never a typed id

A typed Discord id is rejected at `CreateContact`
(`usernotifications/service.go:356`): anyone could bind someone else's snowflake
and have that person DMed with our incidents. Two verified sources only:

1. **A Discord sign-in already on file** (`user_providers`, provider type
   `discord`). The account page shows a one-click **Connect Discord** row, the
   Discord twin of `SlackConnectRow` and of the suggestion built at
   `usernotifications/service.go:248`.
2. **OAuth link mode** for everyone else. Reuse the `/auth/discord/login`
   round trip
   ([server.go:915-919](../../server/internal/app/server.go)) with a `link`
   intent carried in `state`: `identify email` scopes, **no** session, **no**
   `findOrCreateOrganization` — it writes the `user_providers` row for the
   already-signed-in user and returns to the account page, which then creates
   the contact. A test must prove the negative: a link-mode callback for a
   guild that maps to no org creates nothing and mints no session (the
   2026-08-24 capture in `2026-09-19-01` is exactly that failure on the login
   path).

Because both sources also feed `lookupDiscordIdentities`, an admin's re-sync
picks the member up for free afterwards.

Not in scope unless OAuth link mode proves insufficient: a
`/solidping link <code>` slash command redeeming a short-lived code, the
Telegram `/start <token>` pattern
([telegramcb/handler.go:278](../../server/internal/handlers/telegramcb/handler.go)).
Record the decision in the spec if it is taken.

### 3. Declared-identity fallback for on-call mentions

- `server/internal/identitylink/discord.go` — `DeclaredDiscordIdentity(ctx, db,
  integration, userUID)`. Sources, in order: the `discord` contact, then the
  `user_providers` row. Best-effort, nil on any doubt, like the Slack one.
- Unlike Slack there is **no team scoping**: a snowflake is global, not
  per-guild. State the consequence in the doc comment: a mention only pings a
  member who is in that guild; for anyone else Discord renders `<@id>` as an
  inert mention. The `AllowedMentions` allow-list built by
  `renderDiscordMentions` (`notifications/discord.go:449`) is unchanged.
- Consulted at the three Slack call sites — `mentions.go:301`,
  `identities.go:425`, `usernotifications/service.go:326` — and the admin
  mapping (`user_integration_identities`) still wins when present. Add a test
  that proves the precedence with a positive control (admin mapping set →
  declared identity ignored).
- Account page: the "you will be pinged as …" line
  (`account-notifications-slack-mention.spec.ts` behaviour) gets a Discord
  counterpart, including the "nothing links you to this server" case.
- Integration form: the `discord` variant of `SlackMemberMapping` stays
  picker-less, but its empty state now says *how* a member connects
  themselves (section 2) instead of implying only an admin can act.

### 4. DM destinations tab in the org channel picker

- `DiscordDestinationsResponse` gains `Users []DiscordDestinationUser`: the
  **org members with a resolved Discord identity** (admin mapping, contact, or
  sign-in — the same resolution as section 3). Not the guild member list: that
  needs the privileged `GUILD_MEMBERS` intent and would not say which SolidPing
  account a guild member is anyway.
- Picking a user opens the DM via `CreateDM` at pick time and stores the DM
  channel id in `DiscordSettings.channel_id`, plus a `dm_user_id` so the panel
  can show who it is. Discord DM channels (type `1`) **do not support
  threads**: `DiscordSender.sendViaBot` must skip `StartThreadFromMessage` /
  `UnarchiveThread` for a DM destination and post follow-ups as plain messages
  that reference the original. Test that path explicitly; the current sender
  assumes a guild channel throughout.
- `integration-form.tsx`: channel / DM tabs on the Discord panel, the shape of
  `SlackDestinationPanel` (`:1586`).

### 5. Tests — mirror Slack file for file

Go:

- `opsnotify`: Discord case delivers; `50007` → unavailable (with a positive
  control that a plain 5xx is *not* unavailable).
- `jobtypes`: escalation step pages a Discord contact; severity gate;
  Acknowledge from the DM's button row resolves the incident.
- `usernotifications`: create rejects a typed id; create from sign-in works;
  Test dispatches; `50007` surfaces the specific message.
- `identitylink`: resolution order and admin-mapping precedence.
- `discord`: `CreateDM`; sender DM-channel no-thread path; OAuth link mode
  negatives (no session, no org).
- `support`: Discord DM attributes to the member.
- Both dialects for the migration and the contact store (`make test-postgres`
  covers the Postgres layer; `-short` only exercises SQLite).

dash0 E2E (Playwright, `web/dash0/e2e/`):

| New file | Mirrors | Tests |
|---|---|---|
| `discord-member-mapping.spec.ts` | `slack-member-mapping` | matched / not-found with re-sync; **no** picker rendered for the discord variant (the assertion Slack cannot have); clearing a mapping is the red trash action; mention-on-call switch; destinations 409 state |
| `discord-comment-ingestion.spec.ts` | `slack-comment-ingestion` | off with no preference; reflects stored `all`; turning it on saves `comment_ingestion=all` |
| `discord-gateway-status.spec.ts` | `slack-socket-mode` | not-enabled hint; Connected / Disconnected badges on `server.discord.tsx`; settings saved in the UI are the keys the backend reads |
| `account-notifications-discord.spec.ts` | `account-notifications-slack-mention` | connect row from a sign-in; link-mode entry point; pinged-as line; "nothing links you" case; Test button and its `50007` message |
| `channels-discord-bot.spec.ts` (extend) | `channels-slack-install` | DM tab lists identity-resolved members only; picking one stores `dm_user_id` |

Locales: every new key lands in all four of `web/dash0/src/locales/{de,en,es,fr}/`
and `bun run test:unit` (locale parity + translated defaults) is part of the
gate — a backend enum without its locale key passes build and lint and breaks
every locale.

### 6. Docs

- `web/docs/docs/configuration/notifications.md`, Discord section (`:424`):
  personal DM contact, the two connect paths, the DM tab, and the caveat that
  Discord only delivers a DM to a user who shares a server with the bot or has
  opened one — with the Test button as the way to find out.
- `wiki/discord/README.md` §3: DMs need **no** additional permission or intent;
  say so, since the next reader will assume they do.
- `wiki/runbooks/discord-bot-setup.md`: unchanged — link mode uses the scopes
  the login provider already requests.

## Non-goals

- Guild member listing or the `GUILD_MEMBERS` intent.
- Link unfurls and the App Home tab — Discord has no equivalent.
- An add-check modal — `checks add <url>` already covers it.
- Retiring the legacy webhook mode.

## Open questions

- Should the member-mapping panel offer to *invite* unmapped members to connect
  (an email nudge), or is the empty-state hint enough? Default: the hint.
- Does a DM destination (section 4) need its own `mention_on_call` behaviour,
  or is mentioning meaningless in a DM and simply skipped? Default: skipped,
  with a test.

## Implementation Plan

Resolved before starting, and not re-litigated:

- **Open question 1** (invite unmapped members by email) → **no nudge.** The
  member-mapping empty state explains how a member connects themselves.
- **Open question 2** (`mention_on_call` in a DM) → **skipped**, with a test.
- **Migration number**: the spec says `023`; that is wrong.
  `wiki/conventions/database.md` names `022_v0_30_0` as the OPEN UNRELEASED
  migration for this cycle, so the column is appended to `022` as a new
  `-- SECTION:` block in both dialects, both directions.

### 1. `discord` user contact, delivered by the instance bot

1.1 `models.UserContactTypeDiscord = "discord"` — `Value` is the snowflake.
1.2 `user_contacts.dm_channel_id` (nullable) appended to `022_v0_30_0` as
    `SECTION: user-contact-dm-channel-id`, both dialects, both directions, plus
    the header SECTION list. `UserContact.DMChannelID *string`.
1.3 `db.Service.SetUserContactDMChannel(ctx, contactUID, channelID)` in the
    interface and both stores, so the opened DM channel is cached once.
1.4 `discord.APIError{Status, Code, Message}` — `bot_client.do` now decodes
    Discord's JSON error envelope, which it previously threw away. `Unwrap()`
    keeps `errors.Is(err, ErrUnexpectedStatus)` true for every existing caller.
    `discord.IsCannotDMUser(err)` recognises code `50007`.
1.5 `BotClient.CreateDM(ctx, userID)` → `POST /users/@me/channels`.
1.6 `discord.SendContactDM(ctx, client, store, contact, msg)` — reuses
    `contact.DMChannelID`, opens and caches on a miss, retries once through a
    fresh `CreateDM` when the cached channel is gone.
1.7 `opsnotify`: `Deps.SendDiscordDM`, `dispatchRoute` case → `sendDiscordDM`;
    `opsnotifywire` wires it off `config.Discord.BotConfigured()` and maps
    `50007` onto `ErrMediumUnavailable` so coverage falls through to the next
    route. A plain 5xx stays `failed`.
1.8 `jobtypes`: `channelTokenDiscord`, `severityAllowsDiscord` (the
    email/SMS/Telegram rule, not the voice/WhatsApp one), the token added to
    `severityAllowsPersonTargets`, and `pageDiscord` in a new
    `job_escalation_step_discord.go` posting the incident embed **with**
    `discord.IncidentActionRow`, so Acknowledge from a DM reaches the existing
    interactions endpoint unchanged.
1.9 `usernotifications`: `dispatchTestDiscord`, `contactRequiresSetup` treats
    `discord` like Telegram, and a `50007` on Test surfaces the specific
    "open your DMs / join the server" message rather than a generic failure.
1.10 `support.contactTypeForChannel`: `SupportChannelDiscord` →
     `UserContactTypeDiscord`, so a captured DM attributes to the member.
1.11 dash0: `discord` gets its icon + label in `integration-icon.tsx` and
     `member-coverage.tsx`, so it counts as a real non-email route.

### 2. Connecting Discord is a verified binding, never a typed id

2.1 `CreateContact` rejects `type=discord` → `ErrDiscordContactNotDirect`
    (400 VALIDATION_ERROR), mirroring the Telegram guard.
2.2 `POST …/notification-contacts/discord/connect` creates the contact from the
    `discord` `user_providers` row already on file, born verified.
2.3 `POST …/notification-contacts/discord/link-start` returns the Discord
    authorize URL for everyone else. The `link` intent and the initiating user
    UID are carried in the stored `OAuthState` (`Intent`, `LinkUserUID`), which
    is why the public `/auth/discord/callback` needs no cookie session.
2.4 `/auth/discord/callback` in link mode: exchange + profile only, write the
    `user_providers` row, redirect back. **No `findOrCreateOrganization`, no
    `CompleteOrgLogin`, no session** — proven by test.
2.5 `ListRoutes` gains `discordSuggestion`, the twin of `slackSuggestion`.
2.6 dash0 `account.notifications.tsx`: a `DiscordConnectRow` (one-click from a
    sign-in, link-mode button otherwise), gated on `useDiscordBotEnabled()`.

### 3. Declared-identity fallback for on-call mentions

3.1 `identitylink/discord.go` — `DeclaredDiscordIdentity`. Sources in order:
    the `discord` contact, then the `user_providers` row. **No team scoping** —
    a snowflake is global; the doc comment states the consequence (a mention
    only pings a member who is in that guild, otherwise `<@id>` is inert).
3.2 Consulted at the three Slack call sites: `mentions.go`,
    `identities.go` (`lookupDiscordIdentities`), and
    `usernotifications/service.go` (`buildDiscordMention`). The admin
    `user_integration_identities` row still wins — tested with a positive
    control.
3.3 dash0: the Discord "you will be pinged as …" line, including the
    "nothing links you to this server" case.
3.4 `integration-form.tsx`: the discord member-mapping panel stays
    picker-less, but its empty state now says how a member connects themselves.

### 4. DM destinations tab in the org channel picker

4.1 `DiscordDestinationsResponse.Users` — org members with a resolved Discord
    identity (admin mapping, contact, or sign-in), never the guild member list.
4.2 `POST /integrations/discord/dm` opens the DM at pick time and returns its
    channel id; the form stores `channel_id` + `dm_user_id` in
    `DiscordSettings`. `DiscordSettings.IsDM()`.
4.3 `DiscordSender`: a DM destination skips `StartThreadFromMessage` /
    `UnarchiveThread` entirely and posts follow-ups as plain messages that
    reference the original, and skips `mention_on_call` (open question 2).
4.4 `integration-form.tsx`: Channel / Direct message tabs on the Discord panel,
    shaped like `SlackDestinationPanel`.

### 5. Tests

Go, per package: `opsnotify` (delivery, `50007` → unavailable with a 5xx
positive control), `jobtypes` (paging, severity gate, the DM action row),
`usernotifications` (typed id rejected + sign-in positive control, Test
dispatch, the `50007` message), `identitylink` (resolution order, admin-mapping
precedence), `discord` (`CreateDM`, the DM no-thread sender path, link-mode
negatives: no session and no org), `support` (DM attributes to the member),
plus the contact store and migration in both dialects.

dash0 E2E: `discord-member-mapping.spec.ts`,
`discord-comment-ingestion.spec.ts`, `discord-gateway-status.spec.ts`,
`account-notifications-discord.spec.ts`, and the DM-tab cases added to
`channels-discord-bot.spec.ts`.

Locales: every new key in all four of `de/en/es/fr`.

### 6. Docs

`web/docs/docs/configuration/notifications.md` Discord section, and
`wiki/discord/README.md` §3 stating that DMs need no extra permission or
intent. `wiki/runbooks/discord-bot-setup.md` unchanged.
