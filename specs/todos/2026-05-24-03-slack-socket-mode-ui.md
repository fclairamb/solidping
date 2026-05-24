# Slack Socket Mode: operator UI

Depends on: [`2026-05-24-02-slack-socket-mode-backend.md`](2026-05-24-02-slack-socket-mode-backend.md)

## Context

The backend spec adds a `SlackSocketSupervisor` and exposes
`GET /api/v1/integrations/slack/socket/status`. This spec adds the operator-facing UI
so administrators can enable Socket Mode, store the App-Level Token (`xapp-…`), and
verify the connection is actually live — without tailing logs.

Socket Mode is system-wide (one Slack App → one `xapp-` token → one connection). Its
configuration lives in the `system_parameters` table, not in per-org channel rows. The
existing `server.*` family of admin routes already reads and writes `system_parameters`
via `useSystemParameters` / `useSetSystemParameter` (see `server.auth.tsx`, which handles
`auth.slack.*` OAuth fields). This spec adds a new `server.slack.tsx` tab in that family.

## Honest opinion

`server.auth.tsx` already has a "Slack" auth-provider section. Socket Mode is conceptually
different from auth provider config (it's an integration transport, not a sign-in method),
so a separate `server.slack.tsx` tab is cleaner than appending to the auth page. The
tab sits beside `server.auth.tsx`, `server.mail.tsx`, `server.web.tsx`, etc., and the
pattern is identical: use `useSystemParameters` + `useSetSystemParameter`, surface a
`Switch` and secret inputs.

The `useSlackSocketStatus` hook should poll at 5-second intervals with
`refetchIntervalInBackground: false` so the tab is live when open but idle otherwise.

## Goal

- New route `server.slack.tsx` under `web/dash0/src/routes/orgs/$org/`.
- "Socket Mode" section with a `Switch` bound to `slack.socket_mode_enabled` and a
  masked input for `slack.app_token` (stored with `secret: true`).
- Live status card below the form that polls
  `GET /api/v1/integrations/slack/socket/status` every 5 s.
- Hide the Socket Mode section (show a "Slack integration not enabled" hint) when the
  global `slack.enabled` system parameter is falsy.
- All text via the `server` i18n namespace (same as other `server.*` tabs); add keys in
  all four locales (`en`, `fr`, `es`, `de`).

## Non-goals

- Per-org or per-channel Socket Mode toggle (one system-wide `xapp-` token serves all
  orgs; per-channel is not a Slack concept).
- Modifying `channel-form.tsx` or any per-channel Channel UI.
- Changing the existing auth provider section in `server.auth.tsx`.

## Design

### Route

**New file: `web/dash0/src/routes/orgs/$org/server.slack.tsx`**

```typescript
export const Route = createFileRoute("/orgs/$org/server/slack")({
  component: SlackSettingsPage,
});
```

The `server.tsx` layout file controls the tab strip; add a "Slack" tab link pointing to
`/orgs/$org/server/slack` alongside the existing tabs. Inspect `server.tsx` during
implementation to confirm the tab-add pattern.

### System parameters used

| key | secret | used for |
|---|---|---|
| `slack.enabled` | false | Gate: hide Socket Mode section if false |
| `slack.socket_mode_enabled` | false | Toggle |
| `slack.app_token` | **true** | App-Level Token (`xapp-…`) |

All three are read from `useSystemParameters()`. Writes go through
`useSetSystemParameter()`.

No new API hooks are needed for configuration — the existing hooks cover it.

### Status hook

Add to `web/dash0/src/api/hooks.ts`:

```typescript
export interface SlackSocketStatus {
  enabled: boolean;
  connected: boolean;
  lastConnectedAt?: string;
  lastError?: string;
  teamCount?: number;
}

export function useSlackSocketStatus() {
  return useQuery({
    queryKey: ["slack-socket-status"],
    queryFn: async () =>
      apiFetch<SlackSocketStatus>("/api/v1/integrations/slack/socket/status"),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });
}
```

### Page layout

```
┌─────────────────────────────────────────────────────┐
│  Card: Slack Socket Mode                            │
│  ─────────────────────────────────────────────────  │
│  [Switch] Enable Socket Mode                        │
│                                                     │
│  App-Level Token (xapp-…)          [• • • • • •]  │
│  [Eye icon to reveal / Edit toggle if already set]  │
│                                                     │
│  [Save]                                             │
└─────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────┐
│  Card: Connection Status                            │
│  ─────────────────────────────────────────────────  │
│  Status:   ● Connected  (or ○ Disconnected)         │
│  Teams:    3 workspaces                             │
│  Last connected: 2026-05-24 14:03 UTC               │
│  Last error: —                                      │
└─────────────────────────────────────────────────────┘
```

If `slack.enabled` is false, replace both cards with an `Alert` (informational):
"Slack integration is not enabled. Configure it under Auth settings or set
`SP_SLACK_ENABLED=true`."

### UI primitives to reuse

All sourced from `web/dash0/src/components/ui/` (verify at
`http://localhost:4000/dash0/orgs/default/design-reference` before building):

- `Switch` + `Label` — same pattern as `server.auth.tsx` enabled toggle.
- `Input type="password"` / `Eye` / `EyeOff` toggle — same secret-reveal pattern used in
  `server.auth.tsx` (`visibleSecrets` state, `editingSecrets` state).
- `Card` / `CardHeader` / `CardTitle` / `CardContent` — standard section wrapper.
- `Badge` with a green dot for Connected, grey for Disconnected — same pattern as check
  status badges in the design reference.
- `Alert` / `AlertDescription` — for the "not enabled" fallback.
- `Loader2` — spinner while `isLoading`.

Do **not** create custom components; the existing primitives cover every needed element.

### Masking / secret input behavior

Mirror `server.auth.tsx` exactly:
- If `isSecretStored(key)` (param exists with `secret: true`), render a `• • • • • •`
  placeholder with an "Edit" button that clears and enables input.
- Raw input is `type="password"` with `autoComplete="new-password"`.
- Save button calls `setParam.mutate({ key, value, secret: true })`.
- Token is never rendered in plaintext; `GET /api/v1/system/parameters` returns
  `secret: true` rows without their value.

### i18n keys

Add to `web/dash0/src/locales/{en,fr,es,de}/server.json`:

```json
{
  "slack": {
    "title": "Slack",
    "socketMode": {
      "title": "Socket Mode",
      "description": "Connect to Slack over an outgoing WebSocket instead of public webhooks. Requires an App-Level Token (xapp-…) from the Slack App configuration.",
      "enableLabel": "Enable Socket Mode",
      "appTokenLabel": "App-Level Token",
      "appTokenPlaceholder": "xapp-…",
      "saveButton": "Save",
      "notEnabledHint": "Slack integration is not enabled. Configure it under Auth settings or set SP_SLACK_ENABLED=true.",
      "status": {
        "title": "Connection Status",
        "connected": "Connected",
        "disconnected": "Disconnected",
        "teams": "{{count}} workspace connected",
        "teams_other": "{{count}} workspaces connected",
        "lastConnected": "Last connected",
        "lastError": "Last error",
        "noError": "—"
      }
    }
  }
}
```

Translate `fr`, `es`, `de` equivalents for all keys.

## Files to change

### New files
- `web/dash0/src/routes/orgs/$org/server.slack.tsx` — new admin tab

### Modified files
- `web/dash0/src/routes/orgs/$org/server.tsx` — add "Slack" tab to the tab strip
- `web/dash0/src/api/hooks.ts` — add `SlackSocketStatus` type + `useSlackSocketStatus`
- `web/dash0/src/locales/en/server.json` — add `slack.*` i18n keys
- `web/dash0/src/locales/fr/server.json` — French translations
- `web/dash0/src/locales/es/server.json` — Spanish translations
- `web/dash0/src/locales/de/server.json` — German translations

### New test file
- `web/dash0/e2e/slack-socket-mode.spec.ts` — Playwright tests

### Files that need no change
- `web/dash0/src/components/channels/channel-form.tsx` — per-channel UI, not affected
- `web/dash0/src/api/hooks.ts` hooks for `useSystemParameters` / `useSetSystemParameter`
  — already exist, no signature changes

## Acceptance criteria

- [ ] Navigating to `/orgs/default/server/slack` renders the page without error.
- [ ] When `slack.enabled` is false (or absent), both cards are replaced by the info
  `Alert` and neither form field nor save button is rendered.
- [ ] Enabling the Socket Mode `Switch` and clicking Save calls
  `PUT /api/v1/system/parameters/slack.socket_mode_enabled` with `{ value: true }`.
- [ ] Entering an `xapp-` token and saving calls
  `PUT /api/v1/system/parameters/slack.app_token` with `{ value: "xapp-…", secret: true }`.
- [ ] After saving a token, the field renders a `• • • • •` placeholder (not the token)
  and an "Edit" button; the raw value is never visible in the DOM.
- [ ] The Connection Status card updates within 5 s of toggling (mocked in Playwright).
- [ ] The "Connected" badge is green; "Disconnected" badge is grey.
- [ ] The page is fully usable on a 375 px wide viewport (mobile). No horizontal scroll.

## Playwright tests

**`web/dash0/e2e/slack-socket-mode.spec.ts`**:

```typescript
test.describe("Slack Socket Mode settings", () => {
  test("renders not-enabled hint when Slack is disabled", async ({ page }) => { ... });

  test("can enable socket mode and save token", async ({ page }) => {
    // Mock PUT system parameters
    // Toggle Switch → enabled
    // Enter xapp-test → Save
    // Assert PUT called with correct body
    // Assert token field shows placeholder afterward
  });

  test("status card polls and shows connected badge", async ({ page }) => {
    // Mock GET /api/v1/integrations/slack/socket/status → { enabled: true, connected: true, teamCount: 2 }
    // Assert badge text "Connected"
    // Assert team count rendered
  });

  test("status card shows disconnected badge on error", async ({ page }) => {
    // Mock status → { enabled: true, connected: false, lastError: "network timeout" }
    // Assert badge "Disconnected" and error text visible
  });
});
```

Mirror the existing channel Playwright fixture style from
`web/dash0/e2e/channels.spec.ts` (API mocking via `page.route`).

## Verification

```bash
make lint && make test-dash
```

Manual flow:

```bash
make dev
# Navigate to http://localhost:4000/dash0/orgs/default/server/slack
# 1. Verify "not enabled" hint appears (Slack not configured locally)
# 2. Add slack.enabled = true via system parameters API or env
# 3. Reload — form appears
# 4. Toggle Socket Mode on, enter a fake xapp-test token, Save
# 5. Confirm token field shows placeholder
# 6. Confirm Connection Status card polls (check network tab: GET /socket/status every 5 s)
```

## Risk log

| Risk | Mitigation |
|---|---|
| Status polling fires when the tab is backgrounded, wasting requests | `refetchIntervalInBackground: false` in `useSlackSocketStatus` stops polling on tab switch. |
| App-Level Token appears in browser network tab on GET | Backend `GET /api/v1/system/parameters` returns `secret: true` rows with `value: null` (existing behavior). Never render `value` for secret params. |
| New `server.slack` tab conflicts with an existing route segment | Check `server.tsx` tab map before adding. Route file names in the `server.*` family are TanStack Router path segments — a name collision would cause a build error caught by `bun run build`. |
| French/Spanish/German translations are machine-translated and awkward | Mark as "community-reviewed needed" in the PR description; initial quality is acceptable for operator-facing text. |
