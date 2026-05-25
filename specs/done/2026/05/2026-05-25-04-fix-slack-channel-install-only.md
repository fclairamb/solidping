# Fix: Slack channels can't list destinations — make Slack install-only

## Context

On the channel edit page for any Slack-type channel created via the manual New Channel
form (e.g. `…/orgs/org2/channels/df2150e8-0ff2-4561-8e68-01abeb6f3c6a`), the Slack
destination picker shows "Could not connect to Slack workspace — re-install the bot."
The user cannot list (and thus select) any Slack channel or DM target.

The picker calls `GET /api/v1/orgs/{org}/channels/{uid}/slack/destinations`. The
backend requires a bot token stored in the channel's settings, which is absent because
the channel was created manually rather than via the Slack OAuth install flow.

## Root cause

### How the broken state is reached

`web/dash0/src/routes/orgs/$org/channels.new.tsx:37` lists `"slack"` in `ALL_TYPES`
and renders it alongside other manually-configurable types (webhook, email, …). When a
user selects it they see a form whose Slack panel (`SlackDestinationPanel`) just shows
an OAuth hint text (non-edit-mode path, `channel-form.tsx:327-338`) — but the "Create
channel" button at line 188 is enabled as long as `form?.name` is non-empty, which is
true because `initialName` defaults to `"Slack"` (line 176). Submitting creates a
channel row with `settings = '{}'` and no bot token.

All three Slack channels in the dev DB share this fingerprint (`settings = '{}'`,
`settings_private IS NULL`, name `"Slack"`) — confirming all came from the manual form
rather than from the OAuth install (which names the channel after the workspace team).

### Why the edit page fails

`SlackDestinationPanel` in edit mode calls `useSlackDestinations(org, channelUid)` at
`channel-form.tsx:293`. In `GetDestinations` (`slack/service.go:804-809`):

```go
settings, err := models.SlackSettingsFromJSONMap(conn.Settings)  // {} → AccessToken: ""
...
client := NewClient(settings.AccessToken)  // NewClient("")
```

`ListChannels` then POSTs to Slack with `Authorization: Bearer ` (empty token); Slack
returns `{"ok":false,"error":"invalid_auth"}`. `callAPI` returns an error →
`group.Wait` returns the error → `GetDestinations` returns it → the handler emits
**502 Bad Gateway** (`slack/handler.go:33-38`). The frontend surfaces `isError → true`
and renders the "re-install the bot" message (`channel-form.tsx:374-380`).

### Design constraint

The Slack install is **workspace-first**: `createOrUpdateConnection`
(`slack/service.go:316`) derives the org from the Slack workspace identity and keys
the channel by `team_id`. There is no supported path to attach a workspace token to a
manually-created channel in an arbitrary org. A tokenless Slack channel is permanently
broken.

## Goal

- Slack channels can only originate from the OAuth install; the New Channel form no
  longer creates tokenless stubs.
- A not-yet-connected Slack channel's edit page shows a clear "install the Slack app"
  CTA rather than a misleading 502 error.
- The backend rejects `POST /channels` with `type=slack` so re-introduction via API is
  blocked at the source.
- The three existing tokenless stubs display the not-connected CTA and can be deleted
  from the edit page. No data migration required.

## Non-goals

- Binding a workspace install to an arbitrary org's pre-existing channel (would require
  restructuring the workspace-first OAuth flow).
- Changing the Discord channel flow (Discord uses a manually-set webhook URL — correct).

## Approach

### 1. New Channel form — special-case Slack (`channels.new.tsx`)

Mirror the existing `freebox` branch at line 125. When `type === "slack"`, render a
"Connect Slack workspace" card with an Install button that does a full-page redirect to
`/api/v1/integrations/slack/install?source=dashboard`. Do **not** render `ChannelForm`
or the "Create channel" submit for Slack.

### 2. Edit-page not-connected state (`channel-form.tsx`)

In `SlackDestinationPanel`, detect connection state via `settings.team_id` (a
non-secret field present in the public `settings` returned by `GET /channels/{uid}`
for OAuth-connected channels; absent for tokenless stubs).

When in edit mode but `!isConnected`:
- Render a "This Slack channel isn't connected — install the Slack app to link a
  workspace" card with the same Install button.
- Gate `useSlackDestinations` so it does not fire (avoids the 502 call).

### 3. Hook guard (`hooks.ts`)

Add an optional `enabled` boolean (default `true`) to `useSlackDestinations`:

```ts
export function useSlackDestinations(org: string, channelUid: string, enabled = true) {
  return useQuery({
    queryKey: ["slack-destinations", org, channelUid],
    queryFn: () => apiFetch<SlackDestinationsResponse>(
      `/api/v1/orgs/${org}/channels/${channelUid}/slack/destinations`,
    ),
    enabled: enabled && Boolean(org && channelUid),
    staleTime: 60_000,
  });
}
```

### 4. Backend create guard (`channels/service.go` + `channels/handler.go`)

In `CreateChannel` (line 218, after the `connType` switch), add:

```go
if connType == models.ConnectionTypeSlack {
    return nil, ErrSlackManualCreate
}
```

Add alongside the existing error vars (`service.go` near line 23):

```go
ErrSlackManualCreate = errors.New("slack channels are added by installing the Slack app")
```

Map in `handleError` (`handler.go:182`) to 400 `VALIDATION_ERROR`:

```go
case errors.Is(err, ErrSlackManualCreate):
    return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
        "Slack channels are added by installing the Slack app")
```

Safe: the OAuth flow calls `s.db.CreateChannel` directly (not `channels.Service.CreateChannel`), so the install path is unaffected.

### 5. Backend GetDestinations guard (`slack/service.go` + `slack/handler.go`)

After parsing settings (`service.go:804`), add an early return before the Slack calls:

```go
if settings.AccessToken == "" {
    return nil, ErrSlackNotConnected
}
```

Add alongside `ErrNotSlackChannel` (`service.go:43`):

```go
ErrSlackNotConnected = errors.New("slack channel has no bot token — install via OAuth")
```

In `slack/handler.go` (line 25), add a case before the default:

```go
case errors.Is(err, ErrSlackNotConnected):
    return h.WriteError(writer, http.StatusConflict, base.ErrorCodeChannelNotConnected,
        "Slack channel is not connected — install the Slack app")
```

Add to `server/internal/handlers/base/base.go`:

```go
ErrorCodeChannelNotConnected ErrorCode = "CHANNEL_NOT_CONNECTED"
```

### 6. i18n (`en/channels.json`, `fr/channels.json`)

Add near existing `form.slackOauthHint` (line 73 of `en/channels.json`):

```json
"slackNotConnectedTitle": "Slack workspace not connected",
"slackNotConnectedBody": "This channel has no linked Slack workspace. Install the SolidPing Slack app to connect one.",
"slackConnectButton": "Install Slack app"
```

Add French equivalents to `fr/channels.json`.

## Files to edit

| File | Change |
|---|---|
| `web/dash0/src/routes/orgs/$org/channels.new.tsx` | Add Slack branch (Install CTA, no manual form) |
| `web/dash0/src/components/channels/channel-form.tsx` | Not-connected CTA in `SlackDestinationPanel`; pass `enabled` to hook |
| `web/dash0/src/api/hooks.ts` | Add `enabled` param to `useSlackDestinations` (~line 2859) |
| `web/dash0/src/locales/en/channels.json` | Add 3 i18n keys |
| `web/dash0/src/locales/fr/channels.json` | Add 3 i18n keys (FR) |
| `server/internal/handlers/channels/service.go` | Add `ErrSlackManualCreate`; reject in `CreateChannel` |
| `server/internal/handlers/channels/handler.go` | Map `ErrSlackManualCreate` → 400 in `handleError` |
| `server/internal/integrations/slack/service.go` | Add `ErrSlackNotConnected`; guard `AccessToken == ""` in `GetDestinations` |
| `server/internal/integrations/slack/handler.go` | Map `ErrSlackNotConnected` → 409 in error switch |
| `server/internal/handlers/base/base.go` | Add `ErrorCodeChannelNotConnected` |

## Tests

### Backend (table-driven, `testify/require`, `t.Parallel()`)

`server/internal/handlers/channels/service_test.go`:

```go
func TestCreateChannelRejectsSlackType(t *testing.T) {
    t.Parallel()
    // set up test org + db
    _, err := svc.CreateChannel(ctx, org.Slug, CreateChannelRequest{Type: "slack", Name: "x"})
    r.ErrorIs(err, ErrSlackManualCreate)
}
```

`server/internal/integrations/slack/service_test.go`:

```go
func TestGetDestinationsRejectsTokenlessChannel(t *testing.T) {
    t.Parallel()
    // insert a slack channel with settings = {}
    _, err := svc.GetDestinations(ctx, org.Slug, channel.UID)
    r.ErrorIs(err, ErrSlackNotConnected)
}
```

### E2E Playwright (`web/dash0/e2e/channels-slack-install.spec.ts`, new)

- **Slack tile shows Install CTA**: navigate to `/orgs/test/channels/new`, click the
  Slack tile, assert an "Install Slack app" button is visible with an `href` matching
  `/api/v1/integrations/slack/install`; assert no "Create channel" submit button.
- **Unconnected Slack edit page shows CTA**: navigate to the edit route of a tokenless
  Slack channel (create one in test setup via direct DB insert or API with a future
  workaround), assert the not-connected message is visible and no
  `.../slack/destinations` network request is made.

## Verification

1. `make build` — backend compiles cleanly.
2. `make lint` — zero violations.
3. `make test` — all backend tests pass including the two new ones.
4. `make dev-test` — server + dash0 hot-reload in test mode.
5. Manual — New Channel → Slack: shows the "Install Slack app" card; there is no
   "Create channel" button.
6. Manual — navigate to `…/orgs/org2/channels/df2150e8-0ff2-4561-8e68-01abeb6f3c6a`:
   shows "Slack workspace not connected — install the Slack app" CTA instead of the
   broken picker; delete from here.
7. API — `POST /api/v1/orgs/org2/channels` `{"type":"slack","name":"x"}` → 400
   `VALIDATION_ERROR`.
8. API — `GET …/orgs/org2/channels/df2150e8-…/slack/destinations` → 409
   `CHANNEL_NOT_CONNECTED`.
9. `make test-dash` (Playwright) — e2e green.

## Implementation plan

1. **Backend: base error code + channel create guard** — add `ErrorCodeChannelNotConnected`
   to `base/base.go`; add `ErrSlackManualCreate` and reject in `channels/service.go
   CreateChannel`; map in `channels/handler.go handleError`. Write
   `TestCreateChannelRejectsSlackType`. `make test && make lint`.

2. **Backend: GetDestinations guard** — add `ErrSlackNotConnected` to
   `slack/service.go` and early-return when `AccessToken == ""`; map to 409 in
   `slack/handler.go`. Write `TestGetDestinationsRejectsTokenlessChannel`. `make test
   && make lint`.

3. **Frontend: hook + CTA components** — update `useSlackDestinations` with `enabled`
   param; add the Slack branch to `channels.new.tsx`; add not-connected CTA to
   `SlackDestinationPanel`; update i18n EN + FR. `make lint`.

4. **E2E** — write `channels-slack-install.spec.ts`. `make test-dash`.

5. **Archive** — move spec to
   `specs/done/2026/05/2026-05-25-04-fix-slack-channel-install-only.md`.

## Implementation Plan

1. **Backend errors + create guard** — add `ErrorCodeChannelNotConnected` to
   `base/base.go`; add `ErrSlackManualCreate` to `channels/service.go` and reject
   `type=slack` in `CreateChannel` (after the type switch); map to 400
   `VALIDATION_ERROR` in `channels/handler.go handleError`.
2. **Backend GetDestinations guard** — add `ErrSlackNotConnected` to
   `slack/service.go`; early-return when `settings.AccessToken == ""` in
   `GetDestinations`; map to 409 `CHANNEL_NOT_CONNECTED` in `slack/handler.go`.
3. **Backend tests** — `TestCreateChannelRejectsSlackType` (channels) and
   `TestGetDestinationsRejectsTokenlessChannel` (slack).
4. **Frontend hook** — add `enabled = true` param to `useSlackDestinations`.
5. **Frontend New Channel form** — Slack branch in `channels.new.tsx` rendering an
   Install CTA (full-page redirect to `/api/v1/integrations/slack/install?source=dashboard`),
   no `ChannelForm`/submit.
6. **Frontend edit not-connected CTA** — in `SlackDestinationPanel`, detect
   `settings.team_id`; when edit mode but not connected, render the install CTA and
   gate `useSlackDestinations` (pass `enabled={isConnected}`).
7. **i18n** — add `slackNotConnectedTitle`, `slackNotConnectedBody`,
   `slackConnectButton` to `en` + `fr` channels.json.
8. **E2E** — `channels-slack-install.spec.ts`.
9. **QA + archive.**
