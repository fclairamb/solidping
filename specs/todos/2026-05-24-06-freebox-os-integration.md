# Freebox OS integration — foundation (connection + pairing)

## Context

Freebox OS is the router operating system shipped on all Freebox devices made by Free, a French ISP.
It exposes a local HTTP API (`/api/v4/`) that returns real-time data no general-purpose probe can
replicate: xDSL/FTTH line quality, sync-rate drift, SNR margin, error counters, LAN topology, and
system temperatures.

This spec adds the **authentication foundation** — a new `freebox` connection type, the app-pairing
lifecycle, and the per-connection settings model. The check type that actually uses this data is
defined in the follow-up spec
[`2026-05-24-07-freebox-line-quality-check.md`](2026-05-24-07-freebox-line-quality-check.md).

### What Freebox OS is not

- It is not cloud-accessible by default. The API runs on the LAN (`http://mafreebox.freebox.fr`
  resolves only on-network). Remote access is possible but requires a separate manual toggle in the
  Freebox admin (custom domain + HTTPS port + Freebox-issued cert).
- It cannot send notifications outbound. There is no SMS send or "place a call" API. This
  integration is a **source only**, not a notification sink.
- It is not a generic router protocol — this covers Free France's Freebox only, not OPNsense,
  pfSense, or UniFi. The already-shipped SNMP checker covers those.

## Honest opinion

The auth friction is real and should be documented honestly in the UI, not papered over:

- **Initial pairing requires physical access** to the Freebox. The user must press the right-arrow
  button on the LCD front panel within ~30 s of starting the pairing flow. This is a deliberate
  Freebox security model and there is no workaround.
- **For SaaS users running solidping remotely**, they must also enable "Freebox OS remote access"
  in the Freebox settings and configure a public hostname. This is a multi-step manual process.
  The UI must explain this up front.
- **For self-hosters with solidping on the same LAN**, the pairing works with zero extra config.
  That is the primary intended audience for v1.
- The best available Go library is `NikolaLohinski/free-go` (~3 stars, built for a Terraform
  provider, broad API surface, well-tested). It should be vendored. Plan to fork if it goes stale.

Despite the friction, the connection type is worth building because it unlocks the line-quality check
type — a unique, high-value monitoring signal with no alternative.

## Goal

- A `freebox` `ConnectionType` in the integration connection model.
- A pairing flow: request `app_token` from the Freebox, poll until the user approves on the LCD,
  then store the token encrypted.
- Per-connection settings holding the base URL and the pairing state.
- A thin client wrapper in `server/internal/integrations/freebox/` used by check executors.
- A frontend form to create/edit a Freebox connection with clear UX for the LCD pairing step.

## Non-goals

- xDSL/FTTH line-quality check type (spec 2).
- LAN host discovery (spec 3).
- Freebox remote-access setup automation (user does it manually in the Freebox admin).
- Notification delivery via Freebox (no API for that).
- Support for multiple Freebox devices per connection (one device per connection in v1).

## Freebox OS auth lifecycle

The Freebox auth is challenge-based with a one-time LCD-approval step:

### Step 1 — Request app_token (one-time, requires LCD)

```
POST /api/v4/login/authorize/
Content-Type: application/json

{
  "app_id":      "io.solidping",
  "app_name":    "SolidPing",
  "app_version": "1.0.0",
  "device_name": "SolidPing"
}
```

Response:
```json
{
  "success": true,
  "result": {
    "app_token": "dyNYgfK0Ya6FWGqq83sBHa7TGUYj+R+I...",
    "track_id": 42
  }
}
```

The `app_token` is permanent (survives Freebox reboots). Store it immediately, encrypted.
The Freebox LCD now shows a pairing prompt — the user must press right-arrow within ~30 s.

### Step 2 — Poll pairing status

```
GET /api/v4/login/authorize/{track_id}
```

Response `result.status` cycles through:
- `unknown` — not yet approved
- `pending` — user has not pressed the button yet
- `granted` — success, `app_token` is activated
- `denied` — user rejected
- `timeout` — LCD prompt expired

Poll every 2 s until `granted`, `denied`, or `timeout`.

### Step 3 — Open a session (before each API call block)

```
GET /api/v4/login/
→ { "result": { "challenge": "VzhbtpR4r8CLaJle2QgJcs..." } }

POST /api/v4/login/session/
{ "app_id": "io.solidping", "password": hmac_sha1(app_token, challenge) }
→ { "result": { "session_token": "35JYdQSvkOBn...", "permissions": {...} } }
```

Session tokens expire after a period of inactivity. The client wrapper must handle
`auth_required` errors and transparently re-open the session. Session tokens are ephemeral and
**not** stored in the DB.

## Data model

### Connection type

`server/internal/db/models/integration.go` — add alongside existing types:

```go
ConnectionTypeFreebox ConnectionType = "freebox"
```

### Public settings (JSONB in `integration_connections.settings`)

```go
type FreeboxSettings struct {
    BaseURL    string `json:"baseUrl"`    // default: "http://mafreebox.freebox.fr"
    AppID      string `json:"appId"`      // "io.solidping"
    DeviceName string `json:"deviceName"` // user-visible label for the Freebox admin
    TrackID    int    `json:"trackId,omitempty"` // only present during pairing; cleared on grant
    Status     string `json:"status"`     // "pairing" | "granted" | "denied" | "timeout"
}
```

### Private settings (AES-256-GCM in `integration_connections.settings_private`)

`app_token` only. Follows the existing envelope in `server/internal/crypto/credentials/`.

```go
type FreeboxPrivateSettings struct {
    AppToken string `json:"appToken"`
}
```

No migration to the `integration_connections` schema is required — the table uses JSONB and a
TEXT private column and is type-agnostic. Only the `ConnectionTypeFreebox` constant and the
settings struct need to be added.

## Client wrapper

New package `server/internal/integrations/freebox/`:

### `client.go`

```go
type Client struct {
    baseURL    string
    appToken   string // permanent, decrypted at runtime
    httpClient *http.Client
    sessionMu  sync.Mutex
    session    string // current session_token, refreshed on auth_required
}

func NewClient(baseURL, appToken string) *Client

// Authorize starts the pairing flow; returns app_token + track_id.
func (c *Client) Authorize(ctx context.Context) (*AuthorizeResult, error)

// PollPairing queries status until granted/denied/timeout; caller polls.
func (c *Client) PollPairing(ctx context.Context, trackID int) (string, error) // returns status

// Get performs an authenticated GET, transparently renewing the session.
func (c *Client) Get(ctx context.Context, path string, out any) error
```

Wrap `NikolaLohinski/free-go` for the low-level Freebox API calls. Vendor the library.

### `service.go`

Higher-level operations used by handlers:

```go
func StartPairing(ctx context.Context, settings *FreeboxSettings) (*AuthorizeResult, error)
func CheckPairingStatus(ctx context.Context, settings *FreeboxSettings, trackID int) (string, error)
func ValidateConnection(ctx context.Context, settings *FreeboxSettings, privateSettings *FreeboxPrivateSettings) error
```

### Permissions required

The pairing request must specify what permissions the app needs. For v1, request at minimum:

```json
{ "settings": false, "contacts": false, "calls": false, "files": false,
  "explorer": false, "pvr": false, "home": false, "parental": false,
  "player": false, "tv": false, "vm": false, "records": false,
  "download": false }
```

Explicit permission needed: `Monitoring` (read connection stats, system info). Check the Freebox
OS SDK docs for the exact permission name — it may be `"settings": true` or a separate key added
in API v4.

## HTTP handlers

### CRUD

The existing `server/internal/handlers/channels/` pattern handles CRUD for `IntegrationConnection`
records, including encryption/decryption of `settings_private`. No structural change needed there —
the handler calls the model layer which is already type-agnostic.

### Pairing-specific endpoints

Add two lightweight endpoints under the channels handler or a new freebox sub-handler:

```
POST /api/v1/orgs/:org/integrations/freebox/pair
```

Body: `{ "baseUrl": "http://mafreebox.freebox.fr" }`

- Calls `StartPairing` → gets `app_token` + `track_id`.
- Creates or updates the `IntegrationConnection` record with `status: "pairing"` and
  `settings.trackId = track_id`. Stores `app_token` encrypted immediately.
- Returns `{ "trackId": 42, "connectionUid": "..." }`.

```
GET /api/v1/orgs/:org/integrations/freebox/pair/{connectionUid}/status
```

- Calls `CheckPairingStatus` with the stored `track_id`.
- On `granted`: clears `track_id` from settings, sets `status: "granted"`.
- Returns `{ "status": "pending" | "granted" | "denied" | "timeout" }`.

The frontend polls this endpoint every 2 s while showing the LCD prompt instructions.

## Frontend

### Channel form (`web/dash0/src/components/channels/channel-form.tsx`)

Add a `FreeboxForm` variant. The form has two phases:

**Phase 1 — Base URL input + pair button**

```
┌─────────────────────────────────────────────────────┐
│  Freebox base URL                                   │
│  [http://mafreebox.freebox.fr              ]        │
│  (Leave as-is if solidping runs on the same LAN.   │
│   For remote access, use your Freebox hostname.)   │
│                                                     │
│  [Pair with Freebox]                               │
└─────────────────────────────────────────────────────┘
```

**Phase 2 — LCD prompt (shown after pair request succeeds)**

```
┌─────────────────────────────────────────────────────┐
│  Go to your Freebox and press → on the LCD          │
│                                                     │
│  ◉ Waiting for approval…   [Cancel]                 │
│                                                     │
│  (Your Freebox will show a pairing prompt for ~30s) │
└─────────────────────────────────────────────────────┘
```

Frontend polls the status endpoint every 2 s. On `granted`, transitions to the normal channel
detail view. On `denied`/`timeout`, shows an error with a retry option.

Check `http://localhost:4000/dash0/orgs/default/design-reference` for the loading spinner and
status-step primitives before implementing anything custom.

### Channel icon

Add a Freebox icon to `web/dash0/src/components/channels/channel-icon.tsx`. Use the Freebox brand
SVG or a generic router icon as a fallback.

### i18n

`web/dash0/src/locales/en/channels.json` and `fr/channels.json`:
- `"freebox.name"` — "Freebox"
- `"freebox.description"` — "Monitor your Freebox line quality and network status"
- `"freebox.baseUrl"` — "Freebox base URL"
- `"freebox.baseUrlHint"` — (LAN vs remote explanation)
- `"freebox.pairButton"` — "Pair with Freebox"
- `"freebox.waitingApproval"` — "Waiting for LCD approval…"
- `"freebox.pressArrow"` — "Press → on your Freebox LCD"
- `"freebox.pairingTimeout"` — "Pairing timed out — retry"
- `"freebox.pairingDenied"` — "Pairing was rejected on the Freebox"

## Files to create / modify

### New files
- `server/internal/integrations/freebox/client.go`
- `server/internal/integrations/freebox/service.go`
- `server/internal/integrations/freebox/types.go` (FreeboxSettings, FreeboxPrivateSettings, API response shapes)
- `web/dash0/src/components/channels/freebox-form.tsx`

### Modified files
- `server/internal/db/models/integration.go` — add `ConnectionTypeFreebox`
- `server/internal/handlers/channels/handler.go` — register pairing endpoints
- `web/dash0/src/components/channels/channel-form.tsx` — dispatch to `FreeboxForm`
- `web/dash0/src/components/channels/channel-icon.tsx` — add freebox icon case
- `web/dash0/src/locales/en/channels.json` — i18n keys
- `web/dash0/src/locales/fr/channels.json` — i18n keys
- `go.mod` / `go.sum` — add `github.com/NikolaLohinski/free-go`

## Verification

```bash
make lint && make test
```

Manual flow (requires a physical Freebox on the same network):

1. Start solidping in test mode: `make dev-test`
2. Create a Freebox connection via the UI.
3. Click "Pair with Freebox" — the LCD prompt and spinner should appear.
4. Press → on the Freebox LCD within 30 s.
5. UI transitions to "granted" state, connection row appears in the channel list.
6. Restart the server; confirm the connection is still usable (session re-opens transparently).
7. Create a dummy check using `connectionUid` (or wait for spec 2) and confirm
   `GET /api/v4/connection/` returns 200 via the client wrapper.

For remote-access path: configure Freebox remote API manually, use the custom hostname in the
base URL field, confirm pairing and API access still work.
