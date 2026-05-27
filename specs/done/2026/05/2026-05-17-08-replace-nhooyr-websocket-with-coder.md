# Replace nhooyr.io/websocket with github.com/coder/websocket

## Context

`nhooyr.io/websocket` was archived and its maintenance transferred to Coder Inc.
The successor package `github.com/coder/websocket` is a drop-in replacement with
the same API (same original author, same import surface). `staticcheck` correctly
flags every call site in the codebase as deprecated, and the current workaround
is `//nolint:staticcheck` comments scattered across
`server/internal/checkers/checkwebsocket/checker.go` and `checker_test.go`.

## Goal

Replace the import in the two affected files, remove all the `//nolint:staticcheck`
suppressions, run `go mod tidy`, and confirm lint + tests still pass.

## Out of scope

- No behavior change — the WebSocket checker behaves identically after the swap.
- No API change — no config fields, HTTP endpoints, or response shapes change.
- No other packages or modules are affected.

## Implementation

### 1. Update the dependency

```bash
cd server
go get github.com/coder/websocket@latest
go mod tidy
```

This replaces `nhooyr.io/websocket v1.8.17` in `go.mod` (line 81) and refreshes
the two corresponding lines in `go.sum` (lines 674–675).

### 2. `server/internal/checkers/checkwebsocket/checker.go`

- Change the import `"nhooyr.io/websocket"` → `"github.com/coder/websocket"`.
- Remove every `//nolint:staticcheck // using nhooyr.io/websocket v1` comment
  (currently on lines 13, 102, 132, 142, 168, 176, 181, 197).

### 3. `server/internal/checkers/checkwebsocket/checker_test.go`

- Same import swap.
- Remove all `//nolint:staticcheck` comments on import and call sites.

No other files require changes.

## Verification

```bash
cd server
go mod tidy
make lint   # no staticcheck deprecation warnings; nolintlint must not flag orphaned directives
make test   # all TestWebSocketChecker_Execute_* tests pass
```

Optional smoke test against a live server (requires `make dev` running):

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create a WebSocket check against a public echo server
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"WS Echo","slug":"ws-echo","type":"websocket",
       "config":{"url":"wss://echo.websocket.org","send":"ping","expect":"ping"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq .
```

Wait one polling cycle and verify the check shows `up` in the dashboard.
