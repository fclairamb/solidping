---
sidebar_position: 8
title: Live Dashboard Updates
---

# Live Dashboard Updates

The dashboard updates in real time: check status changes, new incidents,
fresh results, and background-job progress appear within a couple of seconds
— no manual refresh, no aggressive polling.

## How it works

The server pushes **hint events** over a Server-Sent Events stream
(`GET /api/v1/orgs/:org/events/stream`). Hints carry no data — only which
resource kinds changed (`results`, `checks`, `incidents`, `events`, `jobs`).
The dashboard invalidates the matching caches and refetches through the
normal REST API.

Because hints are advisory, delivery is best-effort by design: a missed hint
is corrected by the next hint, by a lazy fallback poll (every few minutes
while the stream is healthy), or by a full `resync` after a reconnect. If the
stream cannot connect at all, the dashboard silently falls back to its
regular polling intervals.

On multi-replica deployments the hints ride PostgreSQL `LISTEN`/`NOTIFY`, so
a result ingested by one API replica reaches dashboards connected to every
other replica. Each replica holds exactly **one** LISTEN session regardless
of how many dashboard tabs are open. Single-node SQLite deployments get the
same behavior through in-process channels.

High-volume kinds (results, jobs) are coalesced server-side to at most about
one hint per organization per second per instance; status transitions and
incident lifecycle events are pushed immediately.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_REALTIME_ENABLED` | `true` | Enable the live hint stream. When `false`, the endpoint returns 404 and the dashboard keeps polling. |
| `SP_REALTIME_FLUSH_INTERVAL` | `1s` | Coalescing window for high-volume hints (per org, per instance). |
| `SP_REALTIME_PING_INTERVAL` | `25s` | SSE keep-alive comment interval. |
| `SP_REALTIME_MAX_CONNECTIONS` | `1000` | Maximum concurrent streams per instance (0 = unlimited). |

## Consuming the stream yourself

The stream is plain SSE and works with `curl`:

```bash
curl -N -H "Authorization: Bearer $TOKEN" \
  'https://your-instance/api/v1/orgs/default/events/stream'
```

```text
event: hello
data: {"protocol":1}

event: hint
data: {"kinds":["results"]}

event: hint
data: {"kinds":["checks","incidents","events"]}
```

Events:

- `hello` — emitted once at connect; `protocol` identifies the wire format.
- `hint` — one or more resource kinds changed; refetch what you care about.
- `resync` — the server may have missed notifications (its database listener
  reconnected); refetch everything once.
- Comment lines (`: ping`) are keep-alives; ignore them.

The server closes the stream when your access token expires — reconnect with
a fresh token. Hints carry no history, so reconnecting and refetching once is
always correct.

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
| `solidping_realtime_connections` | gauge | Currently open hint streams |
| `solidping_realtime_hints_published_total` | counter | Hint events published to the bus |
| `solidping_realtime_hints_coalesced_total` | counter | Hint publications absorbed by the coalescer |
| `solidping_realtime_hints_delivered_total` | counter | Hint deliveries to connected streams |
