# Incident notifications — read API and UI

## Context

[2026-05-17-02-incident-notifications-audit-table.md](2026-05-17-02-incident-notifications-audit-table.md)
adds the `incident_notifications` table and write-path instrumentation. This spec exposes
that data through a REST API, a dashboard tab on the incident detail page, a "my pages"
view for individual users, and an MCP tool.

**Prerequisite**: Spec 02 (table + write-path) must be shipped and baked in production
before this spec is implemented.

## Goal

- Operators can view the full per-recipient notification timeline for any incident directly
  in the dashboard, without dropping to SQL.
- Individual users can see a chronological list of incidents they were paged for.
- The agent surface (Claude via MCP) can answer "who got paged for incident X?" in
  natural language.

## List filter and read methods

Extend `server/internal/db/service.go` with:

```go
ListIncidentNotificationsFilter struct {
    IncidentUID   string
    UserUID       string        // optional; omit to return all
    ConnectionUID string        // optional
    Status        string        // optional; e.g. "sent"
    Limit         int           // default 100, max 500
    Before        time.Time     // cursor: rows created before this time
}

ListIncidentNotifications(ctx context.Context, orgUID string, f ListIncidentNotificationsFilter) ([]*models.IncidentNotification, error)
```

The list query joins `users` (for `user_uid IS NOT NULL` rows — to surface `users.name`
inline) and `integration_connections` (for `connection_uid IS NOT NULL` rows — to surface
`integration_connections.name` and `connection_type`). Return the joined fields in a
response DTO rather than raw UIDs, so the API is immediately useful without extra
round-trips from the caller.

Add matching implementations to:
- `server/internal/db/postgres/incident_notification.go`
- `server/internal/db/sqlite/incident_notification.go`

## REST endpoints

New handler package `server/internal/handlers/incidentnotifications/`. Follow the
`escalationpolicies/` layout (`handler.go` + `service.go`). Wire in
`server/internal/app/server.go` after the existing incident routes.

### `GET /api/v1/orgs/:org/incidents/:uid/notifications`

Returns all notification rows for one incident, newest first.

Query parameters: `status`, `userUid`, `connectionUid`, `limit` (default 100).

Response:

```json
{
  "data": [
    {
      "uid": "…",
      "incidentUid": "…",
      "eventType": "incident.escalated",
      "source": "escalation_user",
      "stepUid": "…",
      "repeatIndex": 0,
      "user": { "uid": "…", "name": "Alice Doe" },
      "connection": null,
      "channelType": "email",
      "status": "sent",
      "skipReason": null,
      "error": null,
      "messageId": "<msg-id@solidping>",
      "createdAt": "2026-05-17T10:00:00Z",
      "sentAt": "2026-05-17T10:00:01Z"
    }
  ]
}
```

`user` and `connection` are nullable objects. `connection` carries `{ "uid": "…", "name": "…", "type": "slack" }`.

### `GET /api/v1/orgs/:org/users/:uid/notifications`

Returns rows where `user_uid = :uid` across all incidents, newest first. Query params:
`status`, `limit`, `before` (ISO 8601 cursor for pagination).

Response items include an `incident` sub-object (`{ "uid": "…", "title": "…", "state": "active", "startedAt": "…" }`)
so the caller has enough context to render a linked list without N+1 fetches.

### `GET /api/v1/orgs/:org/me/notifications`

Alias for the above, resolving `:uid` from the authenticated user's JWT. Users can only
see their own rows; admins can query any user via the `:uid` endpoint.

Authorization: `me` — any authenticated member. `:uid` — require `role = admin` if
`uid ≠ self`.

## Dashboard UI

### Notifications tab on incident detail

**File to create / extend**: the incident detail route lives under
`web/dash0/src/routes/orgs/$org/`. Check whether it is structured as a single file or
a nested route set; add a `notifications` child route if nested, or a tab panel if
single-file.

Tab label: **Notifications**. Show a badge count next to the label if any rows exist.

Table columns:

| Column | Notes |
|---|---|
| Time | `createdAt`, relative then absolute on hover |
| Status | Badge: `sent` → green, `failed` → red, `cancelled` → grey, `pending` → yellow, `skipped` → orange |
| Target | User display name (linked to user profile) if `user` set; otherwise channel icon + connection name |
| Source | Human-readable: `"Check connection"`, `"Escalation step N (cycle M)"`, `"On-call schedule"`, `"All admins"` |
| Channel | `email`, `slack`, etc. |
| Error | Show truncated error text; full text on hover/expand. Hidden column when no failures. |

Empty state: "No notifications recorded for this incident yet."

Reuse the table primitive from the design reference at
`web/dash0/src/routes/orgs/$org/design-reference.tsx`. Do not introduce a custom table.

### "My pages" view

New route `web/dash0/src/routes/orgs/$org/me/notifications.tsx`. Accessible from a
sidebar entry (see below) and from the user avatar / profile menu.

Renders a chronological list of incidents the current user was paged for, grouped by day.
Each row shows: incident title (linked), check name, notification time, status badge,
channel type. Infinite-scroll or "Load more" pagination using the `before` cursor.

### Sidebar entry

Add a **My pages** link in the left sidebar. Only show it for authenticated non-bot users.
Display a small count badge if the user has any `pending` rows (active pages in-flight
awaiting ack or delivery confirmation).

## MCP tool

Add to `server/internal/mcp/tools_incidents.go`:

```
Tool name:   incident_notifications_list
Description: List who was notified for a given incident, with delivery status.
Input:       incident_uid (required), status (optional), limit (optional, default 20)
Output:      Array of notification rows with user name / connection name, status, timestamps.
```

This tool allows Claude to answer "who got paged for incident X and did it succeed?"
directly, without the operator having to check the dashboard.

## Files to change

### New files

- `server/internal/db/postgres/migrations/` — `023_incident_notifications.{up,down}.sql` (done in Spec 02)
- `server/internal/db/sqlite/migrations/` — `023_incident_notifications.{up,down}.sql` (done in Spec 02)
- `server/internal/db/models/incident_notification.go` (done in Spec 02)
- `server/internal/handlers/incidentnotifications/handler.go`
- `server/internal/handlers/incidentnotifications/service.go`
- `server/test/integration/incident_notifications_api_test.go`
- `web/dash0/src/routes/orgs/$org/me/notifications.tsx`
- `web/dash0/e2e/incident-notifications.spec.ts`

### Modified files

- `server/internal/db/service.go` — add `ListIncidentNotifications` + filter struct
- `server/internal/db/postgres/incident_notification.go` — add list query (done in Spec 02 for writes; extend here)
- `server/internal/db/sqlite/incident_notification.go` — same
- `server/internal/app/server.go` — wire new handler routes
- `server/internal/mcp/tools_incidents.go` — new MCP tool
- `web/dash0/src/routes/orgs/$org/<incident-detail>` — add Notifications tab
- Sidebar component (check existing nav source — `web/dash0/src/components/` or similar)

## Verification

1. `make build && make lint && make test` — all green.
2. `make test-dash` — new Playwright spec passes.
3. Manual API check with the dev curl recipe from `CLAUDE.md`:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"test","email":"test@test.com","password":"test"}' \
     'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
   curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/test/incidents/<uid>/notifications' | jq .
   ```
4. Open an incident in `make dev-test`, navigate to its detail page, confirm the
   **Notifications** tab appears and shows at least one row after the notification job
   runs.
5. Navigate to the **My pages** view via the sidebar; confirm the incident appears.
6. Verify the MCP tool via the existing MCP test harness if one exists, or manually via
   `server/internal/mcp/`.
7. Test authorization: a non-admin user cannot query another user's notifications via
   `/users/:uid/notifications`.

## Implementation Plan

1. Extend `ListIncidentNotificationsFilter` + `ListIncidentNotifications` in
   `server/internal/db/service.go`.
2. Implement the list query (with user + connection join) in both
   `incident_notification.go` implementations.
3. Write `server/internal/handlers/incidentnotifications/service.go` (sentinel errors,
   `ResolveOrgUID` helper, `ListForIncident`, `ListForUser` methods).
4. Write `server/internal/handlers/incidentnotifications/handler.go` (JSON encode,
   `handleError`, list endpoints for incident + user + me).
5. Wire routes in `server/internal/app/server.go`.
6. Write `server/test/integration/incident_notifications_api_test.go` (list endpoint
   round-trips, authorization check).
7. Add MCP tool in `server/internal/mcp/tools_incidents.go`.
8. Add Notifications tab to the incident detail route.
9. Create `web/dash0/src/routes/orgs/$org/me/notifications.tsx`.
10. Add sidebar entry for **My pages**.
11. Write `web/dash0/e2e/incident-notifications.spec.ts` (tab renders rows; my-pages
    route shows linked incidents).
12. `make lint && make test && make test-dash` — fix any issues.
