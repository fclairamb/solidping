# Cursors on the events and incidents APIs

## Problem

A program that follows an org's alerts (an AI agent, a script, a bridge) has to answer one
question after every restart: "what happened since the last thing I saw?". The REST API
cannot answer it reliably today.

- `GET /api/v1/orgs/{org}/incidents` hands out `pagination.cursor` (the last incident's
  UID) but never reads it back: `ListIncidents` in
  `server/internal/handlers/incidents/service.go` has a `// TODO: Parse cursor` and the
  handler stores `opts.Cursor` for nothing. Asking for page 2 returns page 1 again. The
  storage layer already supports a keyset (`CursorTimestamp` / `CursorUID` on the filter,
  `started_at, uid` in `postgres.go`); only the service is missing.
- `GET /api/v1/orgs/{org}/events` (and the check and incident variants) has a correct
  keyset cursor (`EncodeCursor` / `DecodeCursor` in
  `server/internal/handlers/events/service.go`), but only backwards: newest first, the
  cursor goes to older pages. A consumer cannot say "give me what came after this event".
  `since=<timestamp>` gets close but repeats or skips rows that share a timestamp, and
  the last page has no cursor to resume from.

The live-update WebSocket (`/api/v1/orgs/{org}/events/ws`) tells a client that something
changed, and the ntfy integration pushes alerts. Neither replays what a client missed
while it was disconnected. A forward cursor is the missing piece: push wakes the client up,
the cursor catches it up.

## Proposal

### 1. Incidents: make the existing cursor work

- Parse `cursor` in `ListIncidents` and pass it to the filter as `CursorTimestamp` /
  `CursorUID` (keyset on `started_at, uid`, newest first, as the query already sorts).
- Emit an opaque cursor built like the events one (base64url of
  `RFC3339Nano|uid`). Move `EncodeCursor` / `DecodeCursor` to a shared place (for example
  `server/internal/handlers/base`) instead of copying them.
- A cursor that does not decode (including the bare UIDs handed out until now) starts
  from the first page, as events already do.

### 2. Events: read forward

Add `order` (`desc` default, `asc`) to the three event list endpoints.

- `order=asc` sorts oldest first on `(created_at, uid)`, and `cursor` then means "strictly
  after this event".
- In `asc` mode the response **always** carries `pagination.cursor` for its last row,
  even on the last page, so a consumer stores it and resumes from it next time. An empty
  page returns the cursor it was given, so the consumer never loses its position.
- **Settle window.** Two transactions can commit out of `created_at` order: an event
  stamped 10:00:00.100 can become visible after one stamped 10:00:00.200 that a consumer
  already read past. In `asc` mode, only return events with `created_at` older than
  `now() - 5s`. A consumer sees an event at most 5 s late, never skips one. The window is a
  constant in the service, not a setting.
- `since` / `until` keep working and combine with `cursor`.
- `desc` behaves exactly as today.

### 3. Incidents: read forward

Add the same `order=asc` to the incident list (`started_at, uid`, cursor = strictly after,
cursor always present, same settle window). This lists **new** incidents. Changes to
existing incidents (acked, resolved, reopened) are events: a consumer that wants the full
lifecycle follows `GET /events?type=incident&order=asc`. Say so in the API docs.

### 4. MCP

`list_incidents` accepts `cursor` and `order` and returns the next cursor, so an agent
using MCP can page and resume the same way.

### 5. Docs

- OpenAPI: `order` on the four list operations, `cursor` described as opaque, the
  always-present cursor and the settle window in `asc` mode.
- `wiki/api-specification/` pages for events and incidents.
- A short "Follow events from a program" section in the docs site (where the live-updates
  page is): WebSocket or ntfy for the wake-up, `order=asc&cursor=` for the catch-up, store
  the cursor after each page.

## Non-goals

- No new streaming endpoint (SSE, long poll). The WebSocket hint and ntfy already push.
- No per-consumer state on the server (no stored cursors, no acks). The client keeps its
  cursor.
- No change to retention: events and incidents live as long as they do today.
- No renaming of the events `size` parameter to `limit`.

## Acceptance Criteria

- [ ] Paging through `GET /incidents` with the returned cursor visits every incident exactly
      once, including incidents that share a `started_at` (test with several rows on the
      same timestamp and a page size smaller than the tie group).
- [ ] An old bare-UID cursor on `GET /incidents` returns the first page, not an error.
- [ ] `GET /events?order=asc` walked page by page returns every event exactly once, oldest
      first, with ties on `created_at` (same kind of test).
- [ ] In `asc` mode the last page and an empty page both return a usable cursor; resuming
      from it after inserting new events returns only the new ones.
- [ ] An event newer than the settle window is not returned in `asc` mode and is returned
      by the next call once the window has passed (test with an injected clock, not a sleep).
- [ ] `GET /incidents?order=asc&cursor=` lists only incidents started after the cursor.
- [ ] `desc` responses (default) are unchanged for events and incidents.
- [ ] MCP `list_incidents` takes `cursor` and `order` and returns the next cursor.
- [ ] Works on SQLite and Postgres (`make test` and `make test-postgres`).
- [ ] OpenAPI, wiki and docs updated as described.

## Open questions

- Settle window length. **Recommended: 5 s.** Long enough for any commit the server makes
  in one request, short enough that an agent does not notice.
- Should `order=asc` on events default its starting point to "now" when no cursor and no
  `since` are given (pure tail) instead of the oldest event? **Recommended: no.** Oldest
  first is predictable; a client that wants the tail passes `since=now`.
