# Flat notification route, source breadcrumb, integration notification history

## Context

Notification detail lives at a deeply nested URL that hardcodes the incident as
the only entry point:

```
/orgs/$org/incidents/$incidentUid/notifications/$notificationUid
```

Two things break this:

1. Integrations will also link to notification detail (new "Recent notifications"
   section on the integration page), so the incident UID in the path is no longer
   a stable anchor.
2. The breadcrumb must reflect which surface opened the page (incident or
   integration), but there is no mechanism today to carry that context through the
   URL.

## Naming clarification

Two terms that collide in this context:

- **Events** — the audit log (`/events`, `Event` type, `/api/v1/orgs/:org/events`).
  Indexed by check or incident, not by integration.
- **Notifications** — delivery records (`IncidentNotification`). Already filterable
  by `connection_uid` at the DB layer. These are what the integration page must show.
  Call them "Recent notifications" in UI copy, never "events."

The URL `?source=` also collides: `IncidentNotification.source` is the escalation
source (`"manual"`, `"escalation_policy"`). Use `?from=` as the query param instead.

## Goals

1. Move the notification detail route to a flat path:
   `/orgs/$org/notifications/$notificationUid`
2. Add a `?from=<type>:<uid>` query param (values: `incident:{uid}` or
   `integration:{uid}`) that carries navigation context without coupling the route
   to a single parent.
3. Update the breadcrumb in `$org.tsx` to show the parent entity when `?from=` is
   present, using data already embedded in the notification response (no extra
   API call needed).
4. Add a "Recent notifications" section to the integration detail page
   (`integrations.$integrationUid.tsx`) showing the last ~10 delivery records for
   that integration. Each row links to the notification detail with
   `?from=integration:{integrationUid}`.
5. Update every existing link to notification detail (incident page notification
   rows) to use the new route and append `?from=incident:{incidentUid}`.

## Non-goals

- Changing the notification detail page's content or layout.
- Paginating the integration "Recent notifications" section (a fixed last-10 is
  enough for now).
- Exposing a notification list at the org level beyond what the integration page
  needs.
- Removing the old nested route immediately — keep it alive as a redirect for any
  bookmarked URLs (see Approach).

## Approach

### PR-A — Backend: new org-level notification endpoints

Two new endpoints in a new package `handlers/orgnotifications` (or extend the
existing incidents handler — choose the simpler path):

**1. `GET /api/v1/orgs/:org/notifications/:notifUid`**

Fetches a single notification scoped only by org UID (no incident required).
Reuses the same joined query as `GetIncidentNotification` but without the
`incident_uid` WHERE clause. Returns the same `IncidentNotificationRow` shape.

Auth: existing org-member middleware — same as current incident notifications.

**2. `GET /api/v1/orgs/:org/notifications?connectionUid=&limit=`**

Lists notifications filtered by `connection_uid`. The DB layer already has this
filter (`filter.ConnectionUID` in `ListIncidentNotifications`). Wire it to a new
handler that:
- Requires `connectionUid` query param (return 400 if absent — this endpoint is
  not a general notification inbox)
- Defaults `limit` to 10, max 50
- Returns `{ "data": [...] }` in the standard shape

Register both routes under `/api/v1/orgs/:org/notifications` in `server.go`.
Add tests.

### PR-B — Frontend: route, hooks, breadcrumb, links

**New API hooks** (`api/hooks.ts`):

```typescript
// fetch single notification without incidentUid
useOrgNotification(org: string, notifUid: string)
// → GET /api/v1/orgs/:org/notifications/:notifUid

// list notifications for an integration
useIntegrationNotifications(org: string, integrationUid: string, limit?: number)
// → GET /api/v1/orgs/:org/notifications?connectionUid=&limit=
```

**New route file**:
`web/dash0/src/routes/orgs/$org/notifications.$notificationUid.tsx`

- Uses `useOrgNotification` (not the old incident-scoped hook)
- Reads `Route.useSearch()` to get the `from` param (validated via TanStack
  Router's `validateSearch`)
- Parse helper: `parseFrom(from?: string): { type: "incident" | "integration"; uid: string } | null`
- Back button: if `from` is present, navigate to the parsed parent; otherwise fall
  back to `/incidents` (the most common prior context)
- The page content itself is unchanged from the current implementation

**`?from=` search param shape** (add to route definition):
```typescript
validateSearch: z.object({
  from: z.string().optional(),
})
```

**Redirect the old route** (`incidents.$incidentUid_.notifications.$notificationUid.tsx`):
Replace its component with a redirect that builds the new URL, preserving all
params. Keep this file alive for one release cycle to handle bookmarked URLs.

```typescript
// inside the old route component:
const { org, incidentUid, notificationUid } = Route.useParams();
const navigate = useNavigate();
useEffect(() => {
  navigate({
    to: "/orgs/$org/notifications/$notificationUid",
    params: { org, notificationUid },
    search: { from: `incident:${incidentUid}` },
    replace: true,
  });
}, []);
return null;
```

**Breadcrumb** (`$org.tsx`):

Add an `isNotificationDetail` branch. The notification detail page has the loaded
notification in its query cache; read it directly instead of adding a new
`useQuery` to the breadcrumb:

```
Notification detail (no ?from):
  Bell  Integrations > Notification

Notification detail (?from=incident:{uid}):
  AlertTriangle  Incidents > {incident.title|uid[:8]}  >  Notification

Notification detail (?from=integration:{uid}):
  Bell  Integrations > {connection.name|uid[:8]}  >  Notification
```

To avoid a new fetch in the breadcrumb, use the TanStack Query client's cache:
`queryClient.getQueryData(["orgNotification", org, notificationUid])`. The page
already fetched it; no waterfall.

If the cache is cold (direct navigation, no prior page load), show a shortened
UID as the breadcrumb label — acceptable degradation.

**Update incident page** (`incidents.$incidentUid.tsx`):

Change notification row links from:
```
/orgs/$org/incidents/$incidentUid/notifications/$notificationUid
```
to:
```
/orgs/$org/notifications/$notificationUid?from=incident:{incidentUid}
```

### PR-C — Frontend: integration "Recent notifications" section

Add a `RecentNotificationsSection` component at the bottom of
`integrations.$integrationUid.tsx`:

- Calls `useIntegrationNotifications(org, integrationUid, 10)`
- Shows a table with columns: Status badge, Channel type, Event type, Target
  (user or integration name), Created at, Link icon → notification detail with
  `?from=integration:{integrationUid}`
- Empty state: "No notifications sent through this integration yet."
- Loading state: skeleton rows (3 rows)
- Error state: inline error, no full-page error

The section sits after the form/save button, with a `CardHeader` labelled
"Recent notifications" (not "Recent events").

## Completeness checklist

- [ ] `GET /api/v1/orgs/:org/notifications/:notifUid` endpoint + test
- [ ] `GET /api/v1/orgs/:org/notifications?connectionUid=` endpoint + test
- [ ] `useOrgNotification` hook
- [ ] `useIntegrationNotifications` hook
- [ ] New flat route `notifications.$notificationUid.tsx`
- [ ] Old route replaced with redirect
- [ ] `?from=` search param + `parseFrom` helper
- [ ] Back button respects `?from=`
- [ ] Breadcrumb branch for notification detail
- [ ] Incident page notification links updated
- [ ] Integration page "Recent notifications" section
- [ ] Design reference updated with new breadcrumb example and notification table pattern if new primitives introduced
