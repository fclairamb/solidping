# Slack channel and DM picker

## Context

When a user installs the Slack bot today, the channel-form panel in
`web/dash0/src/components/channels/channel-form.tsx:212-232` is entirely
read-only — it only echoes the workspace name and whatever channel was set by
the `@solidping config default-channel #x` slash command
(`server/internal/integrations/slack/service.go:649`). Users rarely discover
the slash command, so most installs end up with no default channel and silent
incident delivery.

Everything we need is already there on the backend:

- `client.go:317-330` — `ListChannels` calls `conversations.list`
- `client.go` — already calls `users.info`; `users:read` and `users:read.email`
  scopes are requested at install time (`service.go:82-101`)
- `SlackSettings` (`integration.go:96-105`) already stores `ChannelID` /
  `ChannelName`; `notifications/slack.go:104` posts to that ID unchanged
- Slack's `chat.postMessage` accepts a Slack user ID (`U…`) as the channel
  parameter — the API sends it as a DM natively, no extra API call needed

This spec replaces the read-only panel with a live picker: either a public /
private channel the bot can see, or a DM to a specific workspace member.

## Goal

An operator who has installed the Slack bot can open the channel edit page,
see a searchable dropdown of channels and workspace users, pick one, and save
— without ever typing a slash command in Slack.

## Scope

**In scope:**

1. New `SlackSettings` fields `DestinationType` (`"channel"` | `"dm"`) and
   `DisplayName` (the resolved human label — `#alerts` or `@alice`). Stored in
   existing `settings` JSONB; no migration needed. Empty `DestinationType`
   defaults to `"channel"` for backward compat.

2. New `Client.ListUsers(ctx) ([]SlackUser, error)` method in
   `server/internal/integrations/slack/client.go` calling `users.list` (scope
   already granted). `SlackUser{ID, Name, RealName string}`.

3. New API endpoint `GET /api/v1/orgs/:org/channels/:uid/slack/destinations`
   returning `{ channels: [{id, name, isPrivate, isMember}], users: [{id,
   name, realName}] }`. Requires org member auth. Returns 404 if channel does
   not exist or is not type `slack`.

4. Replace `channel-form.tsx:212-232`'s read-only block with:
   - Two-tab strip: **Channel** | **DM**
   - A `Combobox` (from the design-reference primitives) that fetches the
     destinations list lazily on first open
   - Selecting an item calls the parent `update("channel_id", …)` +
     `update("channel_name", …)` + `update("destination_type", …)` — the
     existing PATCH on save picks up the changed settings
   - Skeleton loading state while the fetch is in flight
   - "Bot is not a member of this channel" warning badge when `isMember=false`
     (user needs to `/invite @solidping` in Slack first)

5. `notifications/slack.go` — **no change**. `chat.postMessage` with a `U…`
   user ID works as a DM automatically. The sender is already correctly
   reading `settings.channel_id` (`slack.go:79-88`).

6. Keep the slash command path in `SetDefaultChannel` unchanged (back-compat).
   When called, it sets `DestinationType=""` which the UI and sender both treat
   as `"channel"`.

**Out of scope:**

- Per-user notification routing (Spec B,
  `2026-05-17-07-per-user-notification-routing.md`)
- Listing private channels where the bot is *not* a member — `conversations.list`
  with `types=public_channel,private_channel` only returns channels the bot
  has been added to for private ones; no special handling needed
- Multi-destination per Slack connection (one channel row → one target, as today)
- Workspace members with deactivated Slack accounts — filter `is_bot=false` and
  `deleted=false` from the `users.list` response

## API endpoint

### `GET /api/v1/orgs/:org/channels/:uid/slack/destinations`

No query params. Response:

```json
{
  "channels": [
    { "id": "C0123ABCDE", "name": "alerts",  "isPrivate": false, "isMember": true },
    { "id": "G0234BCDEF", "name": "ops-priv", "isPrivate": true,  "isMember": true }
  ],
  "users": [
    { "id": "U0345CDEFG", "name": "alice",    "realName": "Alice Smith" }
  ]
}
```

Error cases:
- 404 if `channels/:uid` not found or `deleted_at IS NOT NULL`
- 400 if channel type ≠ `slack`
- 502 + wrapped error if Slack API call fails (bot token invalid or revoked)

Wire the route in `server/internal/app/server.go` alongside the existing Slack
integration routes (lines 769-774):
`GET /api/v1/orgs/:org/channels/:uid/slack/destinations` →
`slack.Handler.GetDestinations`.

Handler calls `slack.Service.GetDestinations(ctx, orgUID, channelUID)` which:
1. Loads the channel row, asserts `type="slack"`, decrypts `SettingsPrivate`
2. Creates `NewClient(settings.AccessToken)`
3. Calls `client.ListChannels(ctx)` + `client.ListUsers(ctx)` in parallel (via
   `errgroup`)
4. Returns the combined DTO

## `SlackSettings` changes

`server/internal/db/models/integration.go:96-105` — add two fields:

```go
type SlackSettings struct {
    TeamID            string   `json:"team_id"`
    TeamName          string   `json:"team_name"`
    BotUserID         string   `json:"bot_user_id"`
    AccessToken       string   `json:"access_token"`
    ChannelID         string   `json:"channel_id,omitempty"`
    ChannelName       string   `json:"channel_name,omitempty"`
    DestinationType   string   `json:"destination_type,omitempty"` // "channel" | "dm" | ""
    DisplayName       string   `json:"display_name,omitempty"`     // "#alerts" or "@alice"
    InstalledByUserID string   `json:"installed_by_user_id"`
    Scopes            []string `json:"scopes"`
}
```

`DestinationType == ""` is treated as `"channel"` everywhere. `DisplayName` is
a display-only cache; the canonical identifier is `ChannelID`. No DB migration.

## Frontend changes

`web/dash0/src/components/channels/channel-form.tsx:212-232`

Replace the static `<div className="rounded border…">` with a new component
`<SlackDestinationPanel settings={settings} onChange={update} channelUid={channelUid} org={org} />`.
The component:

- On mount, derives the current tab from `settings.destination_type` (or `"channel"` if empty)
- On tab switch, initialises an empty selection (clear channel_id / channel_name / destination_type)
- Uses the `useSlackDestinations(org, channelUid)` hook to fetch the list lazily
  (disabled until org+channelUid are non-null, i.e. edit page only; new-channel
  form shows a "Save first to configure destination" notice)
- Renders a `Combobox` for the active tab; combobox items are channel.name or
  user.realName with the ID as value
- Writes back: `update("channel_id", selected.id)`, `update("channel_name",
  selected.name)`, `update("destination_type", activeTab)`,
  `update("display_name", activeTab === "channel" ? "#"+name : "@"+name)`

Add `useSlackDestinations(org: string, channelUid: string)` to
`web/dash0/src/api/hooks.ts`:

```ts
export function useSlackDestinations(org: string, channelUid: string) {
  return useQuery({
    queryKey: ["slack-destinations", org, channelUid],
    queryFn: () =>
      apiFetch<SlackDestinationsResponse>(
        `/api/v1/orgs/${org}/channels/${channelUid}/slack/destinations`,
      ),
    enabled: Boolean(org && channelUid),
    staleTime: 60_000,
  });
}
```

`SlackDestinationsResponse` type:

```ts
interface SlackChannel { id: string; name: string; isPrivate: boolean; isMember: boolean; }
interface SlackUser    { id: string; name: string; realName: string; }
interface SlackDestinationsResponse { channels: SlackChannel[]; users: SlackUser[]; }
```

## Edge cases

- **Bot token revoked.** `/slack/destinations` returns 502; the panel shows an
  inline error "Could not connect to Slack workspace — re-install the bot."
- **Bot not in any channel.** `channels` array is empty; the Channel tab shows
  an empty-state hint: "Invite the bot to a channel first with `/invite @solidping`."
- **User selects a DM destination, saves, then switches to Channel tab.** The
  PATCH clears `destination_type` + `channel_id` + `channel_name` only when a
  new selection is made — warn on unsaved change via the existing dirty-state
  mechanism on the edit form.
- **No `channelUid` yet (new-channel creation).** Show a message: "Complete the
  Slack OAuth install to configure the destination." The `SlackDestinationPanel`
  is rendered only after OAuth redirects back to the edit page.

## Verification

- [ ] `make build` — compiles with new `SlackSettings` fields + `ListUsers` method.
- [ ] `make lint` — no new linting errors.
- [ ] Unit test `SlackSender.Send` with a `destination_type="dm"` settings fixture
      containing a `U…` channel_id — verify `PostMessage` is called with the user ID
      unchanged (no regression from existing channel handling).
- [ ] Manual with a real workspace: install bot → open channel edit → Channel tab:
      combobox lists the workspace's public channels; pick one, save, trigger an
      incident, confirm message arrives.
- [ ] Manual: DM tab: combobox lists workspace members; pick one, save, trigger
      incident, confirm DM arrives from the bot.
- [ ] Manual: slash command `@solidping config default-channel #existing` still
      updates the channel and the panel reflects it on next page load.
- [ ] E2E (`web/dash0/e2e/channels.spec.ts` — extend existing file): mock the
      `/slack/destinations` endpoint, select a channel, save, assert PATCH body
      contains `channel_id` + `destination_type`.

## Implementation plan

1. Add `DestinationType` + `DisplayName` to `SlackSettings`
   (`server/internal/db/models/integration.go:96-105`).
2. Add `SlackUser` struct + `Client.ListUsers` method
   (`server/internal/integrations/slack/client.go`).
3. Add `Service.GetDestinations` method
   (`server/internal/integrations/slack/service.go`).
4. Add `Handler.GetDestinations` handler
   (`server/internal/integrations/slack/handler.go`).
5. Register route in `server/internal/app/server.go` near lines 769-774.
6. Add `SlackDestinationPanel` component + `useSlackDestinations` hook on
   the frontend.
7. Swap the read-only Slack panel in `channel-form.tsx:212-232` for
   `<SlackDestinationPanel>`.
8. Unit-test `SlackSender.Send` with DM fixture.
9. Extend `web/dash0/e2e/channels.spec.ts` with the mocked picker E2E case.
10. `make lint && make test && make test-dash`.
