---
model: sonnet
effort: medium
---

# Integration HTTP clients share http.DefaultTransport, so parallel tests break each other's requests

## Problem

Most integration HTTP clients are built as a bare `&http.Client{Timeout: ...}` with no
`Transport`, so they use the process-wide `http.DefaultTransport`.

`httptest.Server.Close()` calls `CloseIdleConnections()` on that global transport. When
tests run in parallel against several `httptest` servers, one test's deferred `srv.Close()`
tears down a connection another test is using:

```
net/http: HTTP/1.x transport connection broken: http: CloseIdleConnections called
```

It looks like a network flake, but it is a real shared-state race, and it only shows up
under CI-level parallelism. It already failed CI in
`server/internal/integrations/twilio` (`TestVerifyCredentials_Rejected`).

That failure was fixed by adding `server/internal/integrations/twilio/httpclient.go`, a
copy of `server/internal/notifications/httpclient.go`: a `sync.Once` that clones
`http.DefaultTransport` into a package-owned transport, plus a `newHTTPClient(timeout)`
helper. Two copies exist now, and every other integration client is still exposed.

Bare clients still on the default transport (`grep -rnE '&http\.Client\{' server/internal/integrations/ | grep -v _test.go`):

| File | Timeout |
|---|---|
| `internal/integrations/ovhsms/client.go:159` | `DefaultTimeout` |
| `internal/integrations/discord/bot_client.go:114` | `DefaultTimeout` |
| `internal/integrations/msteams/client.go:105` | `DefaultTimeout` |
| `internal/integrations/msteams/verify.go:176` | `metadataTimeout` |
| `internal/integrations/msteams/verify.go:239` | `metadataTimeout` |
| `internal/integrations/telegram/client.go:180` | `DefaultTimeout` |
| `internal/integrations/slack/client.go:73` (`NewClientWithBaseURL`) | `DefaultTimeout` |
| `internal/integrations/slack/client.go:101` | `DefaultTimeout` |
| `internal/integrations/slack/client.go:321` | `DefaultTimeout` |
| `internal/integrations/slack/client.go:636` | `DefaultTimeout` |
| `internal/integrations/slack/response_url.go:23` (package var) | `10 * time.Second` |
| `internal/integrations/freebox/client.go:99` | `DefaultTimeout` |
| `internal/integrations/whatsapp/client.go:189` | `DefaultTimeout` |

Checked: `freebox/client.go` sets no custom `Transport` or `TLSClientConfig`, only a
timeout, so it can switch like the others.

## Proposal

### 1. One shared package

Add a small package, e.g. `server/internal/integrations/httpclientpool` (not `httpx`,
which is already the chi router adapter in `internal/httpx`). Exposes:

```go
// NewClient returns an *http.Client with the given timeout, backed by one
// process-wide transport cloned from http.DefaultTransport (not the global itself).
func NewClient(timeout time.Duration) *http.Client
```

Implementation is the existing pattern: a `sync.Once` that clones
`http.DefaultTransport.(*http.Transport)` (falling back to `http.DefaultTransport` if the
assertion fails), with the `//nolint:gochecknoglobals` justification. Move the long
explanatory comment from `notifications/httpclient.go` here, it is the canonical
explanation of why this exists.

Test (`testify/require`, `t.Parallel()`):
- `NewClient` keeps the requested timeout.
- the returned client's `Transport` is not `http.DefaultTransport`.
- two calls share the same `Transport` (one pool, not one per client).

### 2. Remove the duplicates

- `internal/notifications/httpclient.go`: delete; replace its ~15 `newHTTPClient(...)`
  callers with `httpclientpool.NewClient(...)` (or keep a one-line `newHTTPClient` wrapper
  if that keeps the diff smaller; either is fine, no duplicated transport code).
- `internal/integrations/twilio/httpclient.go`: same.

### 3. Switch the remaining bare clients

Replace every entry in the table above with `httpclientpool.NewClient(<same timeout>)`.
Timeouts must stay identical. Re-run the grep afterwards to catch anything that moved:

```bash
grep -rnE '&http\.Client\{' server/internal/integrations/ | grep -v _test.go
```

Expected result: only the one inside `httpclientpool` itself.

### Out of scope

- **Checkers**: they have their own transport in `checkerdef/httptransport.go`. Don't touch.
- Other bare `&http.Client{` outside `integrations/` (same exposure, not asked for here):
  `checkworker/backend/ws.go`, `jmap/client.go`, `jobs/jobtypes/job_webhook.go`,
  `handlers/auth/*_service.go` (8 files), `handlers/checks/importers/betterstack.go`,
  `handlers/feedback/service.go`, `handlers/statussubscribers/notifier.go`. Worth a
  follow-up spec. If the package is placed under `internal/integrations/`, that follow-up
  may want it moved up a level (e.g. `internal/httpclientpool`); consider placing it at
  `internal/` directly now to avoid the move.

## Verification

From `server/`:

```bash
make lint-back        # never relax .golangci.yml
go test -race ./internal/integrations/... ./internal/notifications/...
```

Commit with a `fix:` conventional message.
