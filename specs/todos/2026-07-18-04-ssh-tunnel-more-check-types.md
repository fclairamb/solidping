---
model: opus
effort: high
---

# Only HTTP and TCP checks can run through an SSH tunnel — the classic bastion use cases (databases, queues) cannot

## Problem

The SSH-tunnel declare-and-use flow shipped (specs
`2026-07-16-04-ssh-tunnel-via-check-dependency` and
`2026-07-18-01-ssh-tunnel-ui-declare-and-use`, both done): an SSH check
doubles as the bastion, a check referencing it via the `tunnelCheckUid`
config key gets its probe dialed through the bastion, and the dashboard's
"Run through SSH tunnel" selector renders purely off the server-declared
capability metadata (`CheckTypeInfo.supportsTunnel` —
`web/dash0/src/components/shared/check-form.tsx:281`, no hard-coded type
list anywhere in the frontend).

But only two types declare the capability
(`server/internal/checkers/checkerdef/types.go:265-266`):

```go
{Type: CheckTypeHTTP, ..., SupportsTunnel: true},
{Type: CheckTypeTCP,  ..., SupportsTunnel: true},
```

Server-side validation actively rejects `tunnelCheckUid` on every other
type (`server/internal/handlers/checks/tunnel.go:47`), and the UI never
shows the selector for them. Yet the #1 reason to tunnel is monitoring a
**database or message broker on a private network behind a bastion** —
postgres, mysql, redis, mongodb, rabbitmq, kafka… — none of which can use
the feature today. A user opening a postgres check form finds no tunnel
option and reasonably concludes the UI "doesn't allow declaring and using
an SSH tunnel".

## Proposal

Extend tunnel support to the other TCP-based check types. The plumbing
already exists and is type-agnostic — the worker establishes the tunnel and
injects a `checkerdef.ContextDialer` into the context
(`server/internal/checkers/checkerdef/tunnel.go:41`); a checker opts in by:

1. Routing **every outbound connection** through
   `checkerdef.TunnelDialerFrom(ctx)` when non-nil, keeping the untunneled
   path byte-for-byte unchanged (see `checktcp/checker.go:92` and
   `checkhttp/checker.go:269` for the two shipped examples).
2. **Skipping local name resolution** when tunneled — the bastion resolves
   the hostname; that is the whole point for private names
   (rule documented at `checkerdef/tunnel.go:15`).
3. Setting the `tunneled` output marker like checktcp does.
4. Flipping `SupportsTunnel: true` in `checkerdef/types.go` — the
   dashboard selector and the handler validation follow automatically; no
   per-type frontend work.

### Candidate types and their dialer seams

Verify each seam against the vendored library version before relying on
it; where a library genuinely cannot accept a custom dialer, leave the
type untunneled and record why in the spec archive.

**Stdlib dial sites (mechanical conn swap):**
- `checksmtp` (`checker.go:435-437`), `checkimap`, `checkpop3` — own
  `net.Dialer`; substitute the ctx dialer.
- `checkssl` (`checker.go:84`) — dial via the tunnel, then `tls.Client`
  over the returned conn.

**Client libraries with a dialer option:**
- `checkredis` — go-redis `Options.Dialer`.
- `checkmongodb` — mongo-driver v2 `options.Client().SetDialer(...)`
  (its `ContextDialer` interface matches `checkerdef.ContextDialer`).
- `checkrabbitmq` — amqp091-go `amqp.DialConfig` with a custom `Dial`
  func.
- `checkkafka` — sarama `Config.Net.Proxy.{Enable,Dialer}` (takes an
  x/net/proxy-style dialer).
- `checkgrpc` — `grpc.WithContextDialer`.
- `checkwebsocket` — coder/websocket dials through an `*http.Client`;
  same custom-transport pattern as checkhttp.
- `checkftp` — jlaffaye/ftp `DialWithDialFunc` (note: FTP data
  connections open extra sockets — if the library cannot route those
  through the dialer too, passive-mode data transfer will bypass the
  tunnel; either verify it can, or restrict the tunneled FTP check to
  what genuinely goes through the bastion and document it).
- `checkmqtt` — paho.mqtt.golang `ClientOptions.SetCustomOpenConnectionFn`.

**database/sql drivers (connector-level wiring, the tricky tier):**
- `checkpostgres` — lib/pq: replace the DSN-only `sql.Open` with
  `pq.NewConnector` + `pq.DialerConnector` wrapping the ctx dialer.
- `checkmysql` — go-sql-driver: `mysql.RegisterDialContext` is a
  **process-global registry that never shrinks** — do NOT register
  per-check entries. Register one well-known network name once (e.g.
  `solidping-tunnel`) whose dial func pulls
  `checkerdef.TunnelDialerFrom(ctx)` from the connection context, and
  build the DSN with that network only when tunneled.
- `checkmssql` — go-mssqldb exposes a `Dialer` on its connector.
- `checkoracle` — go-ora: include only if the vendored version exposes a
  connector dialer; otherwise skip with a note.

### Explicit non-goals

- **UDP/ICMP types** — SSH direct-tcpip forwards TCP only: `checkicmp`,
  `checkudp`, `checkntp`, `checksnmp`, `checka2s`, `checkdns`,
  `checkdnsbl`, `checksip`.
- **Passive/synthetic** — `checkheartbeat`, `checksleep`.
- **Own network stack / out of scope for now** — `checkbrowser`,
  `checkjs`, `checkfreeboxline`, `checkdocker`, `checkkubernetes`,
  `checkdomain` (whois), `checkemail`, `checkrdp`, `checkminecraft`,
  `checksftp`/`checkssh` (SSH-in-SSH). Any of these can get their own
  spec later if asked for.

### Tests

Follow the shipped pattern (`checktcp/tunnel_test.go`,
`checkhttp/tunnel_test.go`): the `sshtunneltest` fake bastion + a
`.invalid` hostname that can only resolve remotely, asserting
`srv.Requested()` saw the verbatim `host:port` — this proves both the
tunnel routing and the skipped local lookup without needing a real
protocol backend. For driver-based types where the client insists on a
full protocol handshake, a greeting/handshake-stub listener is enough to
prove the dial path; do not spin up testcontainers per type just for
tunnel routing. Each type also keeps one untunneled-still-resolves-locally
control test like `checktcp`'s.

### Follow-through

- Update the stale "v1 is http + tcp only" comment in
  `server/internal/handlers/checks/tunnel.go:29`.
- Update `web/docs/docs/features/ssh-tunnels.md` (and
  `check-types.md` if it lists tunnel capability) with the new type list.
- E2E stays light: one Playwright assertion that the tunnel selector
  appears on a newly-supported type's form (e.g. postgres) — the
  capability plumbing is already E2E-covered for http/tcp.

## Open questions

- First-pass scope: everything listed under "candidate types" lands in
  one batch, or flagship-first (postgres, mysql, redis, mongodb,
  rabbitmq, kafka) then the rest? Default: implement the full candidate
  list, dropping individual types only where the library seam turns out
  not to exist.

## Implementation Plan

Decision: implement the full candidate list (Open-questions default). Each
type gets the 4-part pattern (route through `checkerdef.TunnelDialerFrom(ctx)`,
skip local name resolution when tunneled, set a `tunneled` output marker, flip
`SupportsTunnel: true`) plus a `sshtunneltest` fake-bastion tunnel test and an
untunneled control test.

### Tier 1 — stdlib dial sites (mechanical conn swap)

| Type | Dialer seam |
|---|---|
| `checksmtp` | `dial()` accepts a `checkerdef.ContextDialer`; tunneled ⇒ skip `resolveHost`, dial raw `host:port` |
| `checkimap` | same shape as smtp (own `net.Dialer`) |
| `checkpop3` | same shape as smtp (own `net.Dialer`) |
| `checkssl` | `tlsConnect()` accepts a dialer; tunneled ⇒ skip `resolveHost`, dial raw `host:port`, then `tls.Client` over the conn |

### Tier 2 — client libraries with a dialer option

| Type | Dialer seam |
|---|---|
| `checkredis` | go-redis `Options.Dialer` |
| `checkmongodb` | mongo-driver v2 `options.Client().SetDialer(...)` (its `ContextDialer` matches `checkerdef.ContextDialer`) |
| `checkrabbitmq` | amqp091-go `amqp.DialConfig{Dial: ...}` |
| `checkkafka` | sarama `Config.Net.Proxy.{Enable,Dialer}` (x/net/proxy-style dialer) |
| `checkgrpc` | `grpc.WithContextDialer` |
| `checkwebsocket` | coder/websocket `DialOptions.HTTPClient` with a tunneled `http.Transport` |
| `checkftp` | jlaffaye/ftp `ftp.DialWithDialFunc` (PASV data sockets: see note below) |
| `checkmqtt` | paho.mqtt.golang `ClientOptions.SetCustomOpenConnectionFn` (or `Dialer`) |

FTP note: jlaffaye/ftp routes BOTH the control connection and PASV data
connections through the `DialFunc`, so passive-mode transfers go through the
tunnel too. The check only opens the control connection anyway.

### Tier 3 — database/sql drivers (connector-level wiring)

| Type | Dialer seam |
|---|---|
| `checkpostgres` | lib/pq `pq.DialerConnector` wrapping the ctx dialer |
| `checkmysql` | go-sql-driver `mysql.RegisterDialContext` — register ONE process-global network name `solidping-tunnel` once whose dial func pulls `TunnelDialerFrom(ctx)`; build DSN with that network only when tunneled |
| `checkmssql` | go-mssqldb connector `Dialer` |
| `checkoracle` | go-ora — verify the vendored v2.9.0 exposes a connector dialer; skip with a documented note if not |

### Follow-through

- Update the stale "v1 is http + tcp only" comment at
  `server/internal/handlers/checks/tunnel.go:29` and the `SupportsTunnel`
  doc-comment in `checkerdef/types.go`.
- Update `web/docs/docs/features/ssh-tunnels.md` and `check-types.md`.
- One Playwright assertion (postgres form shows the tunnel selector).

### Notes on dropped/partial types

(Filled in as implementation proceeds — records any type left untunneled
because the vendored library seam does not exist.)
