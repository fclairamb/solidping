# Results, Incidents & Events

Check results, incident lifecycle and actions, the org-wide audit event stream,
and the live-update WebSocket.

## Results

### GET /api/v1/orgs/:org/results
List monitoring results across checks. Auth: required

Query parameters:
- `checkUid` - comma-separated check UIDs or slugs
- `checkType` - comma-separated check types
- `status` - comma-separated: `up`, `down`, `unknown`
- `region` - comma-separated regions
- `periodType` - comma-separated period types
- `periodStartAfter` - RFC3339 timestamp
- `periodEndBefore` - RFC3339 timestamp
- `with` - comma-separated optional fields
- `cursor` - pagination cursor
- `limit` - page size (default 100, max 1000). Also accepts `?size=` as a deprecated alias.

`pagination` on this endpoint carries only `cursor` and `size` — **no `total`**.
`results` is the largest table in the system; an unbounded `COUNT(*)` scoped to
an organization on every page load can scan tens of millions of rows, so this
endpoint is cursor-only. Page forward by passing the previous response's
`pagination.cursor` back as the `cursor` query parameter; an empty (or absent)
`cursor` means there is no next page. Contrast with incidents below, which
returns a real `pagination.total` from a bounded query.

A single result is fetched through the check-scoped route
`GET /api/v1/orgs/:org/checks/:check/results/:uid` — see [checks.md](checks.md).

## Incidents

### GET /api/v1/orgs/:org/incidents
List incidents. Auth: required

Query parameters:
- `checkUid` - comma-separated check UIDs
- `state` - comma-separated states (e.g., `open`, `resolved`)
- `since` - RFC3339 timestamp
- `until` - RFC3339 timestamp
- `with` - comma-separated: `check`, `members` (`members` also adds `checkGroupSlug`;
  both are opt-in — omitted by default, and the default response costs zero extra
  member/group queries)
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

`pagination.total` is the count of incidents matching the request's filters
(state, `checkUid`, `since`/`until`, `hideSuppressed`, `causedByIncidentUid`),
ignoring `limit`/`cursor` — not the org-wide incident count.

### GET /api/v1/orgs/:org/incidents/:uid
Get a single incident. Auth: required

Query parameters:
- `with` - comma-separated: `check`, `members` (`members` also adds `checkGroupSlug`)

Response extra (detail endpoint only): `attachments[]` — the incident's stored
evidence blobs (spec 2026-08-21-01). Today the only kind is `screenshot`: the
PNG a browser check with `screenshot: true` captured when this incident opened
or reopened. Each entry carries `uid`, `kind`, `name`, `mimeType`, `size`,
`createdAt`, `capturedAt`, `region`, `checkUid`, `trigger`, and a **relative,
short-lived signed** `downloadUrl` (`/pub/files/<uid>?exp=…&sig=…`) — relative
so it resolves against whichever host served the client, re-signed on every
fetch, so do not cache it.

The LIST endpoint never populates `attachments`: signing a URL per attachment
per incident across a whole page is work nobody asked for, and the list view has
nowhere to show one.

Like `details`, this block is **operator-only** and is never serialized onto a
status page or a subscriber payload.

### GET /api/v1/orgs/:org/incidents/:uid/events
List events for a specific incident. Auth: required

Query parameters:
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

## Incident actions

All require auth except the magic-link acknowledgement.

### POST /api/v1/orgs/:org/incidents/:uid/ack
Acknowledge the incident — stops the escalation ladder. Auth: required

### POST /api/v1/orgs/:org/incidents/:uid/unack
Remove an acknowledgement, resuming escalation. Auth: required

### POST /api/v1/orgs/:org/incidents/:uid/snooze
Suppress notifications for a bounded period. Auth: required

### POST /api/v1/orgs/:org/incidents/:uid/unsnooze
Cancel a snooze. Auth: required

### POST /api/v1/orgs/:org/incidents/:uid/resolve
Manually resolve the incident. Auth: required

### GET /api/v1/orgs/:org/incidents/:uid/ack
**Public** one-click acknowledgement from a signed magic link embedded in
notification emails and messages. The signature in the query string is the
credential; there is no session. Returns an **HTML** confirmation page rather
than JSON, since it is opened directly in a browser or mail client.

### POST /api/v1/orgs/:org/incidents/:uid/comments
Add a free-text comment to an incident's timeline. Auth: required.

Creates an append-only `incident.comment` event authored by the calling user
(`source: "web"`) and returns it. Comments also arrive from Slack thread
replies (`source: "slack"`, with Slack author attribution in the payload) and
are read back through `GET …/incidents/:uid/events`. Append-only — no edit or
delete.

Request body:
```json
{ "text": "restarting the pod" }
```
- `text` - comment body, plain text, non-empty after trimming, max 4096 bytes.

Returns `201 Created` with the created event (events-list shape: `uid`,
`incidentUid`, `checkUid`, `eventType`, `actorType`, `actorUid`, `payload`,
`createdAt`). The `payload` carries `text` and `source`. Errors: `400` empty /
over-length text, `404` unknown org or incident.

Incident **notification** history (which routes fired, and when) lives in
[notifications.md](notifications.md).

## Events

### GET /api/v1/orgs/:org/events
List events across the organization. Auth: required

This is also the **audit trail** endpoint — see
[the event catalogue](events-catalogue.md) for every type it can return, what
each payload carries, and the redaction rules.

Query parameters:
- `eventType` - comma-separated event types, matched **exactly**
- `type` - comma-separated event-type **families** (prefixes), e.g.
  `?type=auth,member`. Distinct from `eventType`: `auth` is not a type.
- `actorUserUid` - filter to the events one user caused (`actorUid` is accepted
  as an alias)
- `targetType` / `targetUid` - filter by the acted-on object's kind or identity
  (payload predicates: the target is polymorphic, so it lives in `payload`)
- `sourceIp` - filter by client address. **Admin/owner only**, and silently
  ignored for anyone else rather than rejected — honouring it for a caller who
  cannot see the column would turn the filter into an oracle for it.
- `checkUid` - filter by check UID
- `incidentUid` - filter by incident UID
- `since` / `until` - RFC3339 bounds (`since` inclusive, `until` exclusive)
- `cursor` - pagination cursor. Opaque keyset cursor over
  `(created_at, uid)`; hand back `pagination.cursor` verbatim. An unparseable
  cursor is ignored (first page) rather than rejected.
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

Response items carry `uid`, `eventType`, `actorType`
(`system` | `user` | `api_token` | `service`), `actorUid` plus the resolved
`actorName` / `actorEmail`, `payload`, `createdAt`, and — **for org admins and
owners only** — `sourceIp` and `userAgent`.

**Visibility:** the `auth.*` family is returned to org admins/owners (and super
admins) only. The gate is a server-side filter exclusion, so it holds for an
unfiltered listing, for `?type=auth`, and for `?eventType=auth.login_succeeded`
alike. `sourceIp` / `userAgent` are withheld from non-admins on every event.

### GET /api/v1/orgs/:org/events/ws
Live update hint WebSocket (v2 — per-entity subscriptions; superseded the v1
SSE stream endpoint). Not modelable in OpenAPI (WebSocket); full protocol —
handshake, message table, close codes — documented in prose at
`web/docs/docs/features/live-updates.md`. Summary:

- Registered **outside** the standard auth middleware (browsers can't send
  headers at WS-upgrade time): authenticates in-handler **before** the
  upgrade, via `Authorization: Bearer` header, a `bearer.<jwt>`
  `Sec-WebSocket-Protocol` entry (the SPA's transport — offered alongside
  `solidping.v2`, which the server negotiates back), or the `access_token`
  cookie, in that order. A bad/missing token is an HTTP `401` (no upgrade,
  no in-band auth message). Non-members close `4403`; mid-connection token
  expiry closes `4401`; `SP_REALTIME_ENABLED=false` closes `4404`.
- **Default-silent**: a connection receives nothing until it sends
  `{"type":"subscribe","entity":...}` — `check` (+ `uid`) for one check, or
  `checks`/`incidents`/`events`/`jobs` for the matching org-wide collection.
  Server replies `subscribed` (idempotent on duplicates), then `update`
  frames (`{"entity":...,"uid":...,"kinds":[...]}`) as matching hints arrive,
  plus a `resync` after any bus transport gap.
- Same delivery philosophy as v1: best-effort, hint-only (no data over the
  socket), coalesced high-volume kinds (≤1/org/sec/instance; a burst over 64
  distinct check uids in one window collapses to a wildcard), immediate
  status/incident transitions, lazy client fallback poll, PgBouncer
  session-mode requirement for LISTEN.
- Config knobs: `SP_REALTIME_FLUSH_INTERVAL` (1s), `SP_REALTIME_PING_INTERVAL`
  (25s), `SP_REALTIME_MAX_CONNECTIONS` (1000/instance),
  `SP_REALTIME_AUTH_GRACE` (5s), `SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION`
  (512/connection).
