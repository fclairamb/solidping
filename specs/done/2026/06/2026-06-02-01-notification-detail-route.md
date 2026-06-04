# Notification delivery detail page

## Problem

The incident detail page lists notification delivery attempts in a static,
read-only table — columns Time, Status, Target, Source, Channel, Error
(`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`, `NotificationsCard`
≈ lines 1061–1149). Two limitations:

1. **Rows aren't clickable.** There is no way to open a single delivery and
   inspect it. Nothing changes the URL, so a specific notification can't be
   linked or refreshed to.
2. **The table hides most of what's recorded.** The Error column is
   `max-w-[200px] truncate` with only a hover `title` tooltip, so a message like
   `webhook request failed: status 42…` can't be fully read or copied. The table
   shows only `createdAt`, while the record tracks `sentAt`, `failedAt`, and
   `cancelledAt` separately, plus `eventType`, `stepUid`, `repeatIndex`,
   `messageId`, `skipReason` and the escalation source.

Users need to open a notification and see its full delivery detail on a URL that
can be refreshed and shared.

## Goal

Each notification row opens a dedicated, deep-linkable detail page that shows
everything currently recorded for that delivery attempt.

## Non-goals

- Capturing *new* delivery data (HTTP status code, request/response bodies). That
  is the follow-up — see `2026-06-02-02-capture-notification-delivery-artifacts.md`.
  This spec surfaces only what is **already stored**.
- A detail page reachable from the user-scoped (`/users/:uid/notifications`) or
  me-scoped (`/me/notifications`) lists. Keep the route nested under the incident
  for now; revisit if the user notification views need it.
- Retrying, resending, or editing a notification.

## Routing

Follow the existing nested read-detail precedent
`checks.$checkUid.results.$resultUid.tsx`.

- New route file:
  `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.notifications.$notificationUid.tsx`
- URL: `/orgs/:org/incidents/:incidentUid/notifications/:notificationUid`

Decision: a **dedicated path route**, not a modal/sheet. This matches the CLAUDE.md
convention that detail/editing navigates to a dedicated route, deep-links cleanly,
and survives a page refresh. (A `?notification=<uid>` side-sheet was considered —
it keeps the incident in context and is nice for flipping between rows — but there
is no existing URL-driven drawer pattern in dash0, so we don't introduce one here.)

## Backend

No single-notification endpoint exists today; only the three list endpoints
(`ListForIncident`, `ListForUser`, `ListForMe`). Deep-linking can't rely on the
list being in the client cache (refresh, external link, or a notification beyond
the list `limit`), so add a single GET.

- **Route:** `GET /api/v1/orgs/:org/incidents/:uid/notifications/:notifUid`,
  registered next to the existing `ListForIncident` in
  `server/internal/app/server.go` (≈ line 636).
- **Handler:** `GetForIncident` in
  `server/internal/handlers/incidentnotifications/handler.go`.
- **Service:** `GetForIncident` in
  `server/internal/handlers/incidentnotifications/service.go`; reuse
  `GetIncident` for the org/incident existence check, then fetch the single row.
- **DB:** add `GetIncidentNotification(ctx, orgUID, incidentUID, notifUID)`
  returning the flattened read row (mirror the join in `ListIncidentNotifications`)
  in both `server/internal/db/postgres/incident_notification.go` and
  `server/internal/db/sqlite/incident_notification.go`, plus the `db.Service`
  interface.
- **DTO:** new `NotificationDetail` = the existing `NotificationRow` plus the
  fields stored but not currently exposed: `failedAt`, `cancelledAt`, `jobUid`.
  (`eventType`, `source`, `stepUid`, `repeatIndex`, `channelType`, `status`,
  `skipReason`, `error`, `messageId`, `createdAt`, `sentAt`, and the
  `user`/`connection` sub-objects are already on `NotificationRow`.)
- **Errors:** unknown incident or notification → `base.ErrorCodeNotFound` (404);
  wrong org → 404, not 403 (don't leak existence across orgs).

## Frontend

- `web/dash0/src/api/hooks.ts`:
  - extend `IncidentNotification` (≈ lines 893–910) with `failedAt?: string`,
    `cancelledAt?: string`, `jobUid?: string`;
  - add `useIncidentNotification(org, incidentUid, notifUid)` calling the new GET.
- `NotificationsCard`: make each `<TableRow>` navigate to the detail route
  (wrap in `Link` or row `onClick` → router navigate), with `cursor-pointer` and
  a hover style. Keep keyboard accessibility (row focusable / link semantics).
- Detail page contents:
  - **Header:** status badge (reuse `notificationStatusVariant`), channel type,
    event type.
  - **Delivery timeline:** created → sent / failed / cancelled, from the four
    timestamps; render only the ones present.
  - **Target:** user (link to the user) or connection (type + name, link to the
    integration), or “—”.
  - **Escalation context:** source label (reuse `sourceLabel`), step, escalation
    cycle (`repeatIndex`), and `skipReason` when skipped.
  - **Identifiers:** `messageId`, `jobUid` — copyable.
  - **Error:** full, untruncated, monospace, copyable.
  - **Back link** to the incident.
- Conventions: fully usable on mobile; 403 → “Permission Denied” (no redirect);
  401 → redirect to login with `?returnTo=`.

## Scope

- Frontend: `incidents.$incidentUid.tsx` (clickable rows), new route file,
  `hooks.ts`.
- Backend: `incidentnotifications/handler.go`, `service.go`, `app/server.go`,
  `db/service.go` interface, `db/postgres/incident_notification.go`,
  `db/sqlite/incident_notification.go`.
- Tests: handler + service tests for the new GET (found / not-found / wrong-org);
  Playwright e2e in `web/dash0/e2e/`.

## Acceptance criteria

1. Clicking a notification row navigates to
   `/orgs/:org/incidents/:incidentUid/notifications/:notificationUid` and the URL
   changes.
2. Refreshing that URL loads the detail directly via the single GET (no
   dependence on the list being cached).
3. The full, untruncated error is readable and copyable.
4. All four timestamps render when present; absent ones are hidden — never shown
   as “Invalid Date” or an epoch.
5. An unknown `notificationUid` shows a friendly not-found state, not a crash.
6. The page is usable on mobile.

## Implementation plan

1. **Backend:** db `GetIncidentNotification` (Postgres + SQLite + interface) →
   service `GetForIncident` → handler `GetForIncident` → route registration.
   Add handler/service tests.
2. **Frontend:** extend the type + add the hook; make rows clickable; build the
   detail route/page.
3. **E2E:** click a row, assert the URL change and detail content; assert a
   deep-link refresh renders the same detail.
