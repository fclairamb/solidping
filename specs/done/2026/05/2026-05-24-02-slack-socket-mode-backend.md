# Slack Socket Mode: backend supervisor

## Context

SolidPing's Slack integration currently consumes inbound traffic (events, slash commands,
interactions) only through public HTTPS webhooks at
`/api/v1/integrations/slack/{events,command,interaction}`. This forces every installation
to expose those endpoints to the internet, which rules out:

- Air-gapped / behind-firewall on-prem deployments.
- Developer laptops that don't have a public URL.
- Operators who simply don't want a Slack-callable ingress.

Slack's [Socket Mode](https://api.slack.com/apis/socket-mode) replaces the inbound
webhook with a long-lived, outgoing WebSocket connection from SolidPing to Slack. The app
dials out; no inbound port is required. Outbound notifications (`chat.postMessage`,
`chat.update`, etc.) are unaffected — they stay as direct HTTPS calls through the existing
handwritten `slack.Client` in `server/internal/integrations/slack/client.go`.

## Honest opinion

Socket Mode and HTTPS webhook delivery are mutually exclusive at the Slack App
configuration level — Slack routes events to exactly one transport per app. Keeping both
HTTP handlers registered in SolidPing is correct (they stay in place, receiving no
traffic when Socket Mode is active). Don't gate them in code; let Slack's routing decide.

The JMAP manager in `server/internal/jmap/manager.go` is the cleanest existing model for
a long-running, reconnecting, per-node supervisor. Replicate that pattern (mu / cfg /
Status / `Run(ctx)` / `GetStatus()`) rather than inventing a new one.

The inbound dispatch handlers (`events.go`, `commands.go`, `interactions.go`) are
currently HTTP-transport-entangled. Extracting transport-agnostic `Dispatch*` functions
is a prerequisite for Socket Mode and a correctness win in its own right — both transports
call the same business logic after decoding.

## Goal

- Extend `SlackConfig` with `SocketModeEnabled bool` (`koanf:"socket_mode_enabled"`) and
  `AppToken string` (`koanf:"app_token"`). Expose both via the system-parameters table
  (`slack.socket_mode_enabled`, `slack.app_token`).
- Add `SlackSocketSupervisor` in
  `server/internal/integrations/slack/socketmode.go`, modeled on `jmap.Manager`.
  It dials Slack's Socket Mode endpoint using `github.com/slack-go/slack`'s `socketmode`
  sub-package, reconnects on disconnect, and dispatches events into the transport-agnostic
  `Dispatch*` functions.
- Refactor `events.go`, `commands.go`, `interactions.go` to extract public
  `DispatchEvent(ctx, svc, evt)`, `DispatchCommand(ctx, svc, cmd)`,
  `DispatchInteraction(ctx, svc, interaction)` functions. The existing HTTP handlers
  shrink to: verify signature → parse body → call `Dispatch*`.
- Wire the supervisor in `server/internal/app/server.go` `Start()`, tracked via
  `s.workersWg`, gated by `cfg.Slack.Enabled && cfg.Slack.SocketModeEnabled && cfg.ShouldRunAPI()`.
- Expose `GET /api/v1/integrations/slack/socket/status` returning a `SocketStatus` struct
  for the operator dashboard to poll.
- Pull in `github.com/slack-go/slack`; use only its `socketmode` and `slackevents`
  sub-packages.

## Non-goals

- Per-org / per-channel Socket Mode (the App-Level Token belongs to the Slack App, not
  to any workspace install; one `xapp-` token serves all installed orgs).
- Replacing the outbound handwritten `slack.Client` — `chat.postMessage` stays HTTPS.
- Migrating or disabling the existing HTTPS webhook handlers.
- Leader election for multi-node deployments (the node whose role includes API will run
  the supervisor; two nodes both connecting is benign — Slack delivers to one; document
  as V2).

## Design

### Config extension

**`server/internal/config/config.go`** — extend `SlackConfig` (currently at line 262):

```go
type SlackConfig struct {
    Enabled           bool   `koanf:"enabled"`
    AppID             string `koanf:"app_id"`
    ClientID          string `koanf:"client_id"`
    ClientSecret      string `koanf:"client_secret"`
    SigningSecret     string `koanf:"signing_secret"`
    OAuthCallbackURL  string `koanf:"oauth_callback_url"`
    SocketModeEnabled bool   `koanf:"socket_mode_enabled"`
    AppToken          string `koanf:"app_token"` // xapp-... App-Level Token
}
```

Environment variable names follow the existing `SP_SLACK_*` prefix with koanf's env
mapper:

- `SP_SLACK_SOCKET_MODE_ENABLED=true`
- `SP_SLACK_APP_TOKEN=xapp-…`

System-parameters table overrides (matching the `slack.*` key convention used for
`auth.slack.*` in `server.auth.tsx`):

| key | type | secret |
|---|---|---|
| `slack.socket_mode_enabled` | bool | false |
| `slack.app_token` | string | **true** |

The system-parameters reader path must apply these at startup alongside the other Slack
keys. Check how `auth.slack.*` keys are currently loaded (likely in
`server/internal/app/server.go` or a config-loader helper) and extend it consistently.

### Supervisor

**`server/internal/integrations/slack/socketmode.go`** (new file):

```go
package slack

import (
    "context"
    "log/slog"
    "sync"
    "time"

    slackgo "github.com/slack-go/slack"
    "github.com/slack-go/slack/socketmode"
    "github.com/fclairamb/solidping/server/internal/db"
    "github.com/fclairamb/solidping/server/internal/config"
)

type SocketStatus struct {
    Enabled         bool       `json:"enabled"`
    Connected       bool       `json:"connected"`
    LastConnectedAt *time.Time `json:"lastConnectedAt,omitempty"`
    LastError       string     `json:"lastError,omitempty"`
    TeamCount       int        `json:"teamCount"` // # of org installs currently dispatching
}

type SlackSocketSupervisor struct {
    db     db.Service
    cfg    *config.Config
    log    *slog.Logger

    mu        sync.RWMutex
    connected bool
    lastError string
    lastConn  *time.Time
}

func NewSlackSocketSupervisor(db db.Service, cfg *config.Config, log *slog.Logger) *SlackSocketSupervisor {
    return &SlackSocketSupervisor{db: db, cfg: cfg, log: log}
}

// Run blocks until ctx is cancelled, reconnecting on disconnect.
func (s *SlackSocketSupervisor) Run(ctx context.Context) error { ... }

// GetStatus returns a snapshot suitable for the admin status endpoint.
func (s *SlackSocketSupervisor) GetStatus() SocketStatus { ... }
```

**Run loop sketch**:

1. Build a `socketmode.Client` from the `AppToken` and a `slackgo.Client` using the
   first available org's bot token (for ack/reply; acks don't require a workspace token).
2. Call `socketmode.Client.RunContext(ctx)` in a goroutine.
3. Range over `socketmode.Client.Events` channel; for each event:
   - Ack immediately (required within 3 s).
   - Determine `team_id` from the event payload.
   - Look up the matching `Channel` row via `db.GetChannelByProperty("team_id", teamID)`.
   - Call the appropriate `Dispatch*` function with a `slackService` scoped to that org.
4. On channel close / error, log + sleep 5 s + reconnect.

### Handler refactor

Three files need the same treatment:

**`events.go`** — extract:
```go
// DispatchEvent handles a decoded Slack event payload; transport-agnostic.
func DispatchEvent(ctx context.Context, svc *Service, teamID string, event slackevents.EventsAPIEvent) error
```
`HandleEvents` becomes: verify → unmarshal → `DispatchEvent`.

**`commands.go`** — extract `DispatchCommand(ctx, svc, teamID string, cmd SlashCommand) error`.

**`interactions.go`** — extract `DispatchInteraction(ctx, svc, teamID string, interaction InteractionPayload) error`.

The `svc *Service` parameter is already used internally; the refactor just makes the
inner function addressable from `socketmode.go`.

### Wiring in server.go

In `Server.Start()`, at the block starting near line 1403 (after `jmapManager` is wired):

```go
if s.config.Slack.Enabled && s.config.Slack.SocketModeEnabled && s.config.ShouldRunAPI() {
    s.slackSocketSupervisor = slack.NewSlackSocketSupervisor(s.dbService, s.config, slog.Default())
    s.workersWg.Add(1)
    go func() {
        defer s.workersWg.Done()
        if err := s.slackSocketSupervisor.Run(runnerCtx); err != nil && !errors.Is(err, context.Canceled) {
            slog.WarnContext(runnerCtx, "Slack Socket Mode supervisor exited", "error", err)
        }
    }()
}
```

Add `slackSocketSupervisor *slack.SlackSocketSupervisor` to the `Server` struct
(alongside `jmapManager *jmap.Manager` at line 131).

### Status endpoint

Register in the `slackIntegration` route group (near line 821 in server.go):

```go
slackIntegration.GET("/socket/status", slackHandler.GetSocketStatus)
```

`GetSocketStatus` handler:
- If `s.slackSocketSupervisor == nil`, return `{"enabled":false}`.
- Otherwise return `s.slackSocketSupervisor.GetStatus()`.
- No auth required for the handler itself — the route group can optionally add
  `authMiddleware.RequireAdmin` if desired (prefer to keep it consistent with the JMAP
  status pattern, which does not require auth to read status).

## Files to change

### New files
- `server/internal/integrations/slack/socketmode.go` — supervisor

### Modified files
- `server/internal/config/config.go` — `SlackConfig` new fields
- `server/internal/integrations/slack/events.go` — extract `DispatchEvent`
- `server/internal/integrations/slack/commands.go` — extract `DispatchCommand`
- `server/internal/integrations/slack/interactions.go` — extract `DispatchInteraction`
- `server/internal/integrations/slack/handler.go` — existing handlers call `Dispatch*`
- `server/internal/app/server.go` — struct field, wiring, status route
- `go.mod` / `go.sum` — add `github.com/slack-go/slack`

### Files that need no change
- `server/internal/integrations/slack/client.go` — outbound HTTPS client, untouched
- `server/internal/notifications/slack.go` — `SlackSender`, untouched
- `server/internal/db/models/integration.go` — `SlackSettings` JSONB, untouched
  (App-Level Token is system-wide, not per-channel)

## Tests

**Unit — supervisor** (`server/internal/integrations/slack/socketmode_test.go`):
- Spin a fake WebSocket server implementing the Slack Socket Mode handshake (hello +
  event delivery + ack protocol).
- Assert the supervisor reconnects on forced disconnect.
- Assert `GetStatus()` reports `Connected: true` while connected, `Connected: false`
  after disconnect.

**Unit — dispatch refactor**:
- Table test calling `DispatchEvent` / `DispatchCommand` / `DispatchInteraction` directly
  with mock `Service`. Verifies the business logic is transport-agnostic.
- The existing HTTP-path tests in `handler_test.go` must pass unmodified after the
  refactor — this is the regression guard.

**Integration**:
- Create a channel with a known `team_id`. Deliver a fake Slack event through
  `DispatchEvent` with that `team_id`. Assert the expected DB side-effect (e.g.
  acknowledged slash command creates an incident acknowledgment).

## Verification

```bash
make lint && make test
```

Manual smoke test (real Slack app):

```bash
export SP_SLACK_ENABLED=true
export SP_SLACK_SOCKET_MODE_ENABLED=true
export SP_SLACK_APP_TOKEN=xapp-…      # App-Level Token from Slack App config
export SP_SLACK_CLIENT_SECRET=…       # existing
export SP_SLACK_SIGNING_SECRET=…      # existing
make dev-test
```

1. Install the Slack app in a test workspace.
2. Type `/solidping status` in a channel.
3. Observe the slash command handled in logs and a reply posted to Slack.

Status endpoint:
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

curl -s 'http://localhost:4000/api/v1/integrations/slack/socket/status' | jq .
# Expected: {"enabled":true,"connected":true,"teamCount":1,...}
```

## Risk log

| Risk | Mitigation |
|---|---|
| `slack-go` pulls heavy transitive dependencies | Run `go mod why github.com/slack-go/slack` before merging; only `socketmode` and `slackevents` sub-packages are imported. Review `go mod graph` for unwanted deps. |
| Refactor of `events.go` / `commands.go` / `interactions.go` silently breaks existing behavior | Keep existing HTTP-handler tests passing without modification — they serve as regression guard. Run `make test` before and after the refactor with no other changes. |
| Two API nodes both run the supervisor → Slack delivers each event once, but both nodes ack → possible double-handling | Slack delivers to one connection at a time (socket mode is connection-scoped). Document that multi-API-node deployments should designate one Socket Mode node; add leader election as V2. |
| `app_token` (xapp-…) leaks via logs or status endpoint | `GetStatus()` must never include the token. Ensure `slog` attribute logging in the reconnect loop does not log the token. Store in `system_parameters` with `secret=true`. |
| Slack socket handshake requires specific ack timing (3 s) | Use `slack-go`'s built-in ack; the library handles this. Do not implement a custom ack path. |

## Implementation Plan

1. **Config extension** — add `SocketModeEnabled bool` (`koanf:"socket_mode_enabled"`)
   and `AppToken string` (`koanf:"app_token"`) to `SlackConfig` in
   `server/internal/config/config.go`. Register matching keys in
   `server/internal/systemconfig/systemconfig.go`
   (`auth.slack.socket_mode_enabled`, `auth.slack.app_token`) so the env-var and
   system-parameters reader applies them at startup, matching the existing
   `auth.slack.*` convention used for `signing_secret` / `client_secret`.
2. **Handler refactor** — extract transport-agnostic dispatch entry points so
   HTTP handlers and the Socket Mode supervisor share business logic:
   - `events.go`: add `DispatchEvent(ctx, svc, teamID, event *Event) error`
     and route HTTP `handleEvent` through it.
   - `commands.go`: add `DispatchCommand(ctx, svc, cmd *Command) (*MessageResponse, error)`.
   - `interactions.go`: add `DispatchInteraction(ctx, svc, interaction *Interaction) (*MessageResponse, error)`.
   Existing `*Handler` methods become thin wrappers — they still verify the
   request and decode the payload, then delegate to the new functions.
3. **Add slack-go dependency** — `go get github.com/slack-go/slack` and
   `go mod tidy`. Only the root and `socketmode` sub-packages are needed.
4. **Supervisor** — new file
   `server/internal/integrations/slack/socketmode.go` with `SocketStatus`
   and `SlackSocketSupervisor`. Implements `Run(ctx)` modeled on
   `jmap.Manager`:
   - Builds `socketmode.Client` from `cfg.Slack.AppToken`.
   - Goroutine pumps events from `Client.Events`; each event is acked then
     translated into our internal `Event` / `Command` / `Interaction`
     types and routed through `DispatchEvent` / `DispatchCommand` /
     `DispatchInteraction`.
   - Reconnect-on-error with 5s backoff; updates `connected` / `lastError`
     / `lastConnectedAt` under `sync.RWMutex`.
   - `GetStatus()` returns `SocketStatus` snapshot; never leaks
     `app_token`.
5. **Wiring** — in `server/internal/app/server.go`:
   - Add `slackSocketSupervisor *slack.SlackSocketSupervisor` field on
     `Server`.
   - After the JMAP wiring block in `Start()`, when
     `cfg.Slack.Enabled && cfg.Slack.SocketModeEnabled && cfg.ShouldRunAPI()`,
     instantiate the supervisor and launch it under `s.workersWg` with
     `runnerCtx`.
6. **Status endpoint** — add
   `GET /api/v1/integrations/slack/socket/status` returning the supervisor's
   `SocketStatus` (or `{"enabled":false}` when nil). Handler method
   `GetSocketStatus` on `*slack.Handler`, accepting the supervisor via a new
   constructor parameter (kept optional; tests still call `NewHandler` with
   nil).
7. **Tests**:
   - `dispatch_test.go` — unit tests calling `DispatchEvent` /
     `DispatchCommand` / `DispatchInteraction` directly with mocks; pins
     transport-agnostic contract.
   - `socketmode_test.go` — supervisor unit tests using a fake WebSocket
     server speaking enough of the Slack Socket Mode protocol to assert
     connect → ack → disconnect → reconnect transitions and the
     `GetStatus()` snapshot semantics.
   - The existing HTTP handler tests continue to pass unmodified
     (regression guard for the refactor).
8. **QA** — `make build-backend build-client lint-back test`, fix anything
   that breaks.
9. **Audit + archive** — spawn an Explore subagent for the completeness
   audit, then move the spec into `specs/done/2026/05/`.
