# Email Inbox via JMAP — Foundation

## Goal

Add the infrastructure for SolidPing to receive emails on arbitrary addresses through a single shared JMAP mailbox. This spec covers only the inbox plumbing — turning emails into check results is handled by the next spec (`2026-04-29-02-email-passive-checks.md`).

The deliverable here is a long-running connection manager that:

- Connects to a configured JMAP server (RFC 8620 / 8621) using credentials from a system parameter.
- Watches the inbox via EventSource (RFC 8620 §7.3) with automatic reconnect, falling back to polling when EventSource isn't supported.
- Hands each new email to a pluggable handler (registered by the next spec).
- Moves processed emails to a `Processed` mailbox, leaves unmatched ones in the inbox until cleanup.
- Periodically prunes old emails from `Processed`, the inbox, and `Trash`.
- Exposes status (`connected`, `lastSyncedAt`, `lastError`) and admin-triggered `test` / `sync` operations.

## Why JMAP

- JSON over HTTP — no MIME / IMAP IDLE state machine to maintain.
- Native push via EventSource → real-time delivery without long-polling.
- `Email/changes` for incremental catch-up after disconnects.
- Bearer / basic auth over TLS — no raw IMAP password storage in the message store.
- The subset we use (session, `Email/query`, `Email/get`, `Email/set`, `Mailbox/query`, EventSource) is small enough that a hand-written client in `server/internal/jmap/` is preferable to pulling in a third-party dep.

## Non-goals

- No SMTP submission. Sending email stays out of scope.
- No attachment ingestion. We only need email envelope and (optionally) the body text — payloads are dropped after extracting status metadata in spec 02.
- No per-org JMAP credentials. A single shared mailbox handles every org; routing is done by recipient address (spec 02).
- No support for multiple inboxes. Exactly one JMAP account.

---

## Configuration — `system_parameters`

Stored as a system parameter with `key = "email_inbox"` and `secret = true` (uses the existing `system_parameters` table and `db.Service.GetSystemParameter` / `SetSystemParameter`). Value shape:

```json
{
  "enabled": true,
  "sessionUrl": "https://mail.example.com/.well-known/jmap",
  "username": "ingest@solidping.example",
  "password": "<secret>",
  "addressDomain": "ingest.solidping.example",
  "mailboxName": "Inbox",
  "processedMailboxName": "Processed",
  "pollIntervalSeconds": 60,
  "processedRetentionDays": 30,
  "failedRetentionDays": 7,
  "rewriteBaseUrl": ""
}
```

| Field | Default | Notes |
|-------|---------|-------|
| `enabled` | `false` | Manager idles when false. |
| `sessionUrl` | — | JMAP session endpoint. |
| `username` / `password` | — | Basic auth for JMAP and blob downloads. |
| `addressDomain` | — | Domain used to compute display addresses for checks (e.g. `ingest.solidping.example`). Stored here so the server can show users the right address without the frontend hard-coding it. |
| `mailboxName` | `"Inbox"` | Mailbox to watch. |
| `processedMailboxName` | `"Processed"` | Created on demand if missing. |
| `pollIntervalSeconds` | `60` | Used only when EventSource isn't available (capability missing or `eventSourceUrl` empty). |
| `processedRetentionDays` | `30` | Old `Processed` emails moved to `Trash`. |
| `failedRetentionDays` | `7` | Inbox emails that never matched a handler are eventually moved to `Trash`. |
| `rewriteBaseUrl` | `""` | Override for proxied JMAP setups where the session returns internal URLs. |

`addressDomain` is also exposed (read-only, non-secret) via the existing `GET /api/v1/system/parameters` endpoint so the frontend can render check addresses.

---

## Code layout

### `server/internal/jmap/` (new package)

| File | Contents |
|------|----------|
| `client.go` | `Client` with `NewClient`, `DiscoverSession`, `SetRewriteBaseURL`, `Call`, basic-auth HTTP. |
| `eventsource.go` | SSE reader with reconnect: `ListenEventSourceWithReconnect(ctx, types, handler)` exponential backoff (1s → 5min cap). |
| `methods.go` | Typed wrappers: `MailboxQuery`, `FindMailboxByName`, `FindMailboxByRole`, `FindOrCreateMailbox`, `EmailQuery(filter)`, `EmailGet(ids)`, `EmailGetWithAttachments(ids)`, `EmailSetMailbox(ids, fromID, toID)`, `EmailDestroy(ids)`. |
| `types.go` | `Session`, `Account`, `Request`, `MethodCall`, `Response`, `MethodResponse`, `Email`, `EmailAddress`, `EmailHeader`, `Attachment`, `Mailbox`, `ChangesResponse`, `EventSourceEvent`, `Config`. |
| `manager.go` | The long-running connection manager (see below). |
| `manager_test.go` / `client_test.go` | Unit tests + integration test against a fake server. |

### `server/internal/jmap/manager.go` — `Manager`

```go
type Handler interface {
    HandleEmail(ctx context.Context, m *Mailboxes, email Email) (Outcome, error)
}

type Outcome int

const (
    OutcomeProcessed Outcome = iota // Move to Processed
    OutcomeIgnored                  // Leave in Inbox; cleanup will eventually trash
    OutcomeRejected                 // Move to Processed (don't retry)
)

type Mailboxes struct {
    Inbox     *Mailbox
    Processed *Mailbox
    Trash     *Mailbox
}

type Manager struct {
    db       db.Service
    handlers []Handler
    // private state: mu, config, client, lastSyncedAt, lastError, connected
}

func NewManager(dbService db.Service) *Manager { /* ... */ }
func (m *Manager) RegisterHandler(h Handler)  // called from app wiring
func (m *Manager) Run(ctx context.Context) error
func (m *Manager) GetStatus() Status
func (m *Manager) TriggerSync(ctx context.Context) error
func (m *Manager) TestConnection(ctx context.Context, cfg *Config) error
```

`Run(ctx)` lifecycle (single supervisor goroutine started from `app/server.go`):

1. Load `email_inbox` system parameter; if missing or `enabled=false`, sleep 60s and retry.
2. Discover JMAP session; on failure, set `lastError`, sleep 30s, retry.
3. Resolve `Inbox`, `Processed` (create if missing), `Trash` (best-effort).
4. If `session.eventSourceUrl != ""`, run `runEventSource`; else run `runPolling`.
5. On context cancellation, drain handlers and exit.

`runEventSource` mirrors the pattern used elsewhere: parse the JMAP `StateChange` payload, extract the `Email` state for the discovered account, only sync when it actually changes (ignore `ping` events and unrelated state changes — otherwise a stuck no-handler email gets reprocessed on every keepalive).

`runPolling` does an immediate sync, then ticks every `pollIntervalSeconds`.

`syncEmails` loop:

```
ids ← Email/query { inMailbox: inbox.id }
emails ← Email/get(ids)               // include from, to, subject, receivedAt, messageId, header, body preview
for each email:
   for each registered handler:
       outcome, err := handler.HandleEmail(ctx, mailboxes, email)
       if err != nil: log, continue
       if outcome != OutcomeIgnored:
           Email/set: move to Processed
           break
update lastSyncedAt
periodically: cleanupOldEmails()
```

`cleanupOldEmails`:

- `Processed` older than `processedRetentionDays` → `Trash`.
- `Inbox` older than `failedRetentionDays` → `Trash`.
- `Trash` older than 7 days → `Email/destroy`.

Multiple handlers are supported so spec 02 can plug in without further changes here. First non-`OutcomeIgnored` handler wins.

---

## Handler registration

In `server/internal/app/server.go`, wire after services are constructed:

```go
inboxMgr := jmap.NewManager(s.dbService)
// spec 02 will append: inboxMgr.RegisterHandler(emailCheckHandler)
go func() { _ = inboxMgr.Run(serverCtx) }()
s.services.JMAPInbox = inboxMgr // expose for handlers/admin endpoints
```

`services.Registry` gains a `JMAPInbox *jmap.Manager` field.

---

## Admin endpoints

All under existing super-admin authorization (system parameter management, see `internal/handlers/system/`).

```
GET    /api/v1/system/email-inbox/status
POST   /api/v1/system/email-inbox/test
POST   /api/v1/system/email-inbox/sync
```

| Endpoint | Body | Response |
|----------|------|----------|
| `GET .../status` | — | `{ "enabled": bool, "connected": bool, "lastSyncedAt": iso8601?, "lastError": string? }` |
| `POST .../test` | `{ "sessionUrl": "...", "username": "...", "password": "..." }` (omit to use stored config) | `{ "ok": true, "accountId": "...", "mailboxes": ["Inbox", "Processed", "Trash"] }` |
| `POST .../sync` | — | `{ "ok": true }` (manual catch-up) |

Reuse the existing `internal/handlers/system/` package for handlers; add a `service.go` method that proxies to `Manager`.

---

## Errors

Standard error envelope (see `base.HandlerBase`). New error codes in `internal/handlers/base/`:

- `EMAIL_INBOX_NOT_CONFIGURED` — `email_inbox` parameter missing.
- `EMAIL_INBOX_DISABLED` — `enabled=false`.
- `EMAIL_INBOX_TEST_FAILED` — connection / mailbox query failed during test.

---

## Tests

| Test | Approach |
|------|----------|
| Client request marshalling | Table-driven, compare JSON output against fixtures. |
| EventSource reconnect | Local `httptest` server that drops the connection and emits a state event after reconnect — assert handler called once per real state change, not per ping. |
| Polling fallback | Same fixture server, no `eventSourceUrl` in session → polling kicks in. |
| Multi-handler routing | Two handlers; first returns `OutcomeIgnored`, second returns `OutcomeProcessed` → assert email moved to `Processed` after the second. |
| Cleanup | Insert old emails, assert moves to `Trash` and destroys. |
| Status reporting | Force connection error → `lastError` populated, `connected=false`. |

Integration test mirrors realassets-style: spin up a small in-process fake JMAP server, validate the full discover → sync → process loop.

---

## Verification

```bash
# Configure (super-admin token required)
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"value":{"enabled":true,"sessionUrl":"https://mail.example.com/.well-known/jmap","username":"ingest@example.com","password":"...","addressDomain":"ingest.solidping.example"},"secret":true}' \
  'http://localhost:4000/api/v1/system/parameters/email_inbox'

# Test connection
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/system/email-inbox/test'

# Status
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/system/email-inbox/status'
```

After enabling, the server log should show `JMAP session discovered`, `Starting JMAP EventSource listener` (or `Starting JMAP polling`), and periodic sync log lines when emails arrive — even though, until spec 02 lands, every email lands in `OutcomeIgnored` and stays in the inbox.

---

## Files to create / modify

| File | Status |
|------|--------|
| `server/internal/jmap/client.go` | new |
| `server/internal/jmap/eventsource.go` | new |
| `server/internal/jmap/methods.go` | new |
| `server/internal/jmap/types.go` | new |
| `server/internal/jmap/manager.go` | new |
| `server/internal/jmap/{client,manager}_test.go` | new |
| `server/internal/handlers/system/handler.go` | add `EmailInboxStatus`, `EmailInboxTest`, `EmailInboxSync` |
| `server/internal/handlers/system/service.go` | add corresponding service methods |
| `server/internal/handlers/base/errors.go` (or wherever codes live) | add 3 new codes |
| `server/internal/app/services/registry.go` | add `JMAPInbox *jmap.Manager` |
| `server/internal/app/server.go` | construct manager, start goroutine, register routes |

---

## Implementation Plan

The work breaks into 6 sequential commits, each independently buildable and unit-tested. Each step ends with `make fmt && make build-backend` + (if applicable) `go test` for that package.

### Step 1 — `internal/jmap/types.go` + `internal/jmap/client.go`

- Define `Config`, `Session`, `Account`, `Capability`, `MethodCall`, `Request`, `Response`, `MethodResponse`, `Email`, `EmailAddress`, `EmailHeader`, `Attachment`, `Mailbox`, `EventSourceEvent`, `ChangesResponse`. JSON tags use the JMAP camelCase wire format (no `tagliatelle` exemption needed — JMAP is camelCase).
- `Client` struct with `httpClient`, `username`, `password`, `apiURL`, `eventSourceURL`, `downloadURL`, `accountID`, optional `rewriteBase` (for proxied setups returning internal URLs).
- `NewClient(cfg Config) *Client`.
- `DiscoverSession(ctx) (*Session, error)` — GET `cfg.SessionURL` with basic auth, set `apiURL`/`eventSourceURL`/`accountID` from the response, apply `rewriteBaseURL` rewriting.
- `Call(ctx, []MethodCall) (*Response, error)` — POST `apiURL` with the JMAP envelope.
- Tests: round-trip a sample `Email/get` response through `Response`/`MethodResponse` JSON unmarshal; verify `DiscoverSession` against `httptest` server returning a real-shaped session document.

### Step 2 — `internal/jmap/methods.go`

Typed wrappers over `Client.Call`:

- `MailboxQuery(ctx, accountID, filter)` → list of mailbox IDs.
- `FindMailboxByName(ctx, accountID, name)` → `*Mailbox` or nil.
- `FindMailboxByRole(ctx, accountID, role)` → `*Mailbox` or nil (used for `Trash` discovery).
- `FindOrCreateMailbox(ctx, accountID, name)` → `*Mailbox`. Uses `Mailbox/set` to create when missing.
- `EmailQuery(ctx, accountID, filter)` → `[]string` IDs.
- `EmailGet(ctx, accountID, ids, properties)` → `[]Email`.
- `EmailSetMailbox(ctx, accountID, ids, fromMailboxID, toMailboxID)` — moves emails by toggling `mailboxIds`.
- `EmailDestroy(ctx, accountID, ids)`.
- `EmailChanges(ctx, accountID, sinceState)` → `ChangesResponse`.

Tests: each method against `httptest` fixtures.

### Step 3 — `internal/jmap/eventsource.go`

- SSE reader using `http.NewRequestWithContext` + `Accept: text/event-stream`, manual line-buffered parsing (no third-party SSE dep).
- `ListenEventSourceWithReconnect(ctx, types []string, handler func(EventSourceEvent) error)`.
- Exponential backoff: start 1s, double up to 5min, reset on successful connect.
- Tests: `httptest` server that streams a few events, drops the connection, then resumes — assert handler called only on real `state` events (filter out `ping`).

### Step 4 — `internal/jmap/manager.go`

- `Outcome`, `Mailboxes`, `Status`, `Handler` interface, `Manager` struct.
- `NewManager(dbService)`, `RegisterHandler`.
- `Run(ctx)` supervisor loop per spec (load config from `system_parameters` via `db.Service.GetSystemParameter`, sleep+retry on errors, dispatch to `runEventSource` or `runPolling`).
- `runEventSource(ctx, c *Client)` — listens, calls `syncEmails` on each real `Email` state change.
- `runPolling(ctx, c *Client)` — ticker-driven `syncEmails`.
- `syncEmails(ctx, c *Client, m *Mailboxes)` — query inbox, get emails, run handlers, move per outcome, update `lastSyncedAt`.
- `cleanupOldEmails(ctx, c *Client, m *Mailboxes)` — hourly within the supervisor loop.
- `GetStatus`, `TriggerSync`, `TestConnection`.
- Multi-handler routing: first non-`OutcomeIgnored` wins.
- Tests: end-to-end against an in-process fake JMAP server. Two-handler routing test. Cleanup test. Status test (force connection error). EventSource ping-vs-state filtering.

### Step 5 — system handler endpoints + service wiring

- New error codes in `internal/handlers/base/errors.go` (or wherever): `EMAIL_INBOX_NOT_CONFIGURED`, `EMAIL_INBOX_DISABLED`, `EMAIL_INBOX_TEST_FAILED`.
- `internal/handlers/system/service.go` — methods `EmailInboxStatus`, `EmailInboxTest(cfg *jmap.Config)`, `EmailInboxSync` proxying to a `*jmap.Manager` injected via constructor.
- `internal/handlers/system/handler.go` — three HTTP handlers + route registration in the existing system route group (super-admin authorization).
- `services.Registry` gains `JMAPInbox *jmap.Manager`.

### Step 6 — app wiring + boot

- `internal/app/server.go` constructs `jmap.NewManager(dbService)`, stores on registry, starts `go inboxMgr.Run(serverCtx)`, wires the system handlers' new dependency.
- Make `addressDomain` readable via `GET /api/v1/system/parameters` (it should already be — system parameters with `secret=false` for the visible fields, or expose a derived read-only endpoint). Verify by running the curl from §Verification.

### Step 7 — QA + archive

`make build-backend build-client lint-back test`. Fix anything broken. Move spec to `specs/done/2026/04/2026-04-29-01-email-inbox-jmap.md`. Merge to main.
