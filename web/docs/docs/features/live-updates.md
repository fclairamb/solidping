---
sidebar_position: 8
title: Live Dashboard Updates
---

# Live Dashboard Updates

The dashboard updates in real time: check status changes, new incidents,
fresh results, and background-job progress appear within a couple of seconds
— no manual refresh, no aggressive polling.

## How it works

The server pushes **hint events** over a WebSocket
(`GET /api/v1/orgs/:org/events/ws`). Hints carry no data — only which
resource kind changed and for which scope. A connection receives **nothing
until it subscribes**: the dashboard subscribes to the scopes the current
page cares about (e.g. the org's check collection on the dashboard, one
specific check on its detail page) and invalidates the matching caches when
a hint arrives, refetching through the normal REST API.

Because hints are advisory, delivery is best-effort by design: a missed hint
is corrected by the next hint, by a lazy fallback poll (every few minutes
while a scope is live), or by a full resync of every subscribed scope after a
reconnect. If the socket cannot connect at all, the dashboard silently falls
back to its regular polling intervals.

On multi-replica deployments the hints ride PostgreSQL `LISTEN`/`NOTIFY`, so
a result ingested by one API replica reaches dashboards connected to every
other replica. Each replica holds exactly **one** LISTEN session regardless
of how many dashboard tabs or subscriptions are open. Single-node SQLite
deployments get the same behavior through in-process channels.

Each client decides what a hint is worth refetching. The dashboard's **checks
list** refreshes immediately on a status transition (`checks`), but not on
every individual result (`results`) — a busy organization writes results
continuously, and refetching the whole list per result costs far more than it
buys. Per-run detail on that page (the latency cell, "last checked") instead
refreshes on the page's own 10-second poll, while result-derived views (a
check's result history and availability charts) still refresh straight off the
`results` hint.

High-volume kinds (results, jobs) are coalesced server-side to at most about
one hint per organization per second per instance; status transitions and
incident lifecycle events are pushed immediately. A flush window that
accumulates hints for more than 64 distinct checks in one organization
collapses the list to a wildcard — every watcher of that org's checks
refetches once, which is what a real mass outage would require anyway.

## Subscription model

| Message | Purpose |
|---|---|
| `{"type":"subscribe","entity":"check","uid":"<uuid>"}` | Watch one check (detail pages). |
| `{"type":"subscribe","entity":"checks"}` | Watch the org's check collection: membership + any check's status/results. |
| `{"type":"subscribe","entity":"incidents"}` / `"events"` / `"jobs"` | Watch the corresponding org-level collection. |
| `{"type":"unsubscribe","entity":...,"uid":...}` | Drop a subscription. |

`check` (paired with a uid) is the only per-entity scope; `checks`,
`incidents`, `events`, and `jobs` are org-collection scopes — the org is
implied by the connection, so there's no separate uid to pass. Subscribing
to `checks` also covers per-check results and status transitions across the
whole org; subscribing to one `check` narrows that down to a single entity,
which is what a check detail page wants instead of refetching on every other
check's activity.

Server replies:

| Message | Meaning |
|---|---|
| `{"type":"hello","protocol":2}` | Auth accepted. |
| `{"type":"subscribed","entity":...,"uid":...}` | Subscription ack (duplicate subscribes are idempotent — always acked, never an error). |
| `{"type":"update","entity":"check","uid":"...","kinds":["results"]}` | The subscribed check changed; `kinds` narrows which query roots to invalidate (empty/missing = "all"). |
| `{"type":"update","entity":"checks"}` (etc.) | The subscribed collection changed. |
| `{"type":"resync"}` | Bus transport gap: invalidate every currently subscribed scope once. |
| `{"type":"error","code":"...","title":"...","entity":...,"uid":...}` | Per-message failure (`NOT_FOUND`, `VALIDATION_ERROR`, `CONCURRENCY_LIMITED`). The socket stays open. |

Unknown server→client message types must be ignored by the client (forward
compat); an unknown client→server type gets an `error` reply.

## Handshake

Authentication happens at the **HTTP level, before the WebSocket upgrade** —
the same way as any other authenticated endpoint. The upgrade request is
authenticated from any of these transports (all supported, first match wins):

1. **`Authorization: Bearer <jwt>` header** (CLI, tests, `curl`/`websocat`).
2. **`Sec-WebSocket-Protocol: bearer.<jwt>, solidping.v2`** — the dashboard's
   transport. Browsers cannot set an `Authorization` header on a WebSocket
   upgrade, so the token rides in a `bearer.`-prefixed subprotocol entry
   (offered alongside the plain `solidping.v2` entry, which the server
   negotiates back). This uses the same token as the REST calls, so the two
   can never disagree.
3. **`access_token` cookie** (fallback for cookie-based clients). Note that
   cookies are shared across ports on a host, so another app on the same
   domain can shadow this cookie — which is why the dashboard uses the
   subprotocol instead.

Tokens never go in the URL, and there is **no in-band `auth` message** — a
token supplied any other way is ignored. A present-but-malformed
`Authorization` header is rejected without falling back to the other
transports.

If the token is **missing, invalid, or expired**, the server responds with a
normal **HTTP `401`** and does not upgrade. An explicit-auth client (header)
reads that status directly. A browser cannot read the status of a failed
upgrade — it sees only a generic `1006` — so the dashboard treats any
pre-`hello` failure as a retryable drop, refreshes its token, and reconnects.

Once authenticated, the server replies `hello`. Organization-scope and
feature-disabled checks still run *after* the upgrade and are reported as
close codes (see below), because those are the terminal states a browser must
react to differently and can only read as WebSocket close codes.

## Close codes

WebSocket upgrade failures are invisible to browser JS (only a generic
`1006`), so terminal conditions are signaled by accepting the socket and
closing with an application code:

| Code | Condition | Client reaction |
|---|---|---|
| `4401` | The access token expired *mid-connection* (a missing/invalid token at connect time is an HTTP `401` before the upgrade, not this code) | Reconnect with backoff and a fresh token. |
| `4403` | Authenticated but not a member of this organization | Stop permanently; fall back to polling. |
| `4404` | `SP_REALTIME_ENABLED=false` | Stop permanently; fall back to polling. |
| `1012` | Server shutdown / hub closed | Reconnect with backoff. |

Keepalive: a transport-level ping every `SP_REALTIME_PING_INTERVAL` (default
25s) with a pong deadline — a dead peer is detected and reaped even if it
never writes anything itself.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_REALTIME_ENABLED` | `true` | Enable the live hint WebSocket. When `false`, the socket is still accepted (browsers can't see a rejected upgrade) but immediately closed `4404`, and the dashboard keeps polling. |
| `SP_REALTIME_FLUSH_INTERVAL` | `1s` | Coalescing window for high-volume hints (per org, per instance). |
| `SP_REALTIME_PING_INTERVAL` | `25s` | Transport-level ping keep-alive interval. |
| `SP_REALTIME_MAX_CONNECTIONS` | `1000` | Maximum concurrent connections per instance (0 = unlimited). |
| `SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION` | `512` | Maximum scopes a single connection may subscribe to (0 = unlimited). |

## Consuming the socket yourself

The endpoint is a plain WebSocket and works with
[websocat](https://github.com/vi/websocat):

```bash
websocat -H="Authorization: Bearer $TOKEN" \
  'wss://your-instance/api/v1/orgs/default/events/ws'
```

Then subscribe to whatever you're interested in:

```json
{"type":"subscribe","entity":"checks"}
```

```text
{"type":"hello","protocol":2}
{"type":"subscribed","entity":"checks"}
{"type":"update","entity":"checks","kinds":["results"]}
```

From browser JavaScript, pass the token via the subprotocol list instead of a
header: `new WebSocket(url, ["bearer." + token, "solidping.v2"])`. Clients
that prefer a cookie can send `Cookie: access_token=<jwt>` instead of the
`Authorization` header. A missing or invalid token is rejected with an HTTP
`401` before the upgrade; there is no in-band `auth` message.

The server closes the connection when your access token expires — reconnect
with a fresh token and re-subscribe to whatever scopes you cared about.
Hints carry no history, so reconnecting, re-subscribing, and refetching once
is always correct.

## Operational notes

### PgBouncer and connection poolers

The realtime listener relies on PostgreSQL `LISTEN`, which binds notification
delivery to a specific database session. **PgBouncer in transaction-pooling
mode is not supported for the realtime listener** — the LISTEN session must
be a direct, session-mode connection. Point `SP_DB_URL` at the database
directly, or use session pooling for SolidPing's pool.

Everything else degrades gracefully: if the listener cannot maintain its
session, dashboards fall back to polling.

### Metrics

The `/metrics` endpoint exposes:

| Metric | Type | Description |
|--------|------|-------------|
| `solidping_realtime_connections` | gauge | Currently open realtime connections |
| `solidping_realtime_subscriptions` | gauge | Currently active scope subscriptions across all connections |
| `solidping_realtime_hints_published_total` | counter | Hint events published to the bus |
| `solidping_realtime_hints_coalesced_total` | counter | Hint publications absorbed by the coalescer |
| `solidping_realtime_hints_delivered_total` | counter | Hint deliveries to connected subscribers |
| `solidping_realtime_messages_received_total` | counter (label `type`) | Client→server messages processed, by type |
