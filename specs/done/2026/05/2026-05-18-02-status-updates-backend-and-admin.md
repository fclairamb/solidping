# Status updates: backend and admin UI

## Context

The public status page (`web/status0/`) shows live status and daily availability bars but has no
narrative layer. Operators cannot publish "we're investigating" or "scheduled maintenance" messages
where visitors can read them. Backlog spec
[`specs/backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md`](../../backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md)
foresees a customer-facing incident layer; this spec is the pragmatic slice of that idea — a
`status_updates` table that works today without renaming `incidents → outages`.

A **status update** is a short operator-written post: title, markdown body, optional "read more"
URL, and a kind (investigating / identified / monitoring / resolved / maintenance / info). Updates
are append-only — each post is an immutable record with its own timestamp, so an incident can be
narrated as a sequence: "13:42 Investigating → 13:55 Identified → 14:10 Resolved". Updates are
always anchored to a specific **status page**, and optionally scoped to a section, a check, or
linked to an auto-detected incident.

This spec covers everything invisible to the public: database, admin API, admin UI in dash0.
Public rendering is in [2026-05-18-03-status-updates-public-timeline.md](2026-05-18-03-status-updates-public-timeline.md).

## Goal

- `status_updates` table with SQLite and Postgres migrations.
- CRUD API under `/api/v1/orgs/:org/status-updates` (authenticated, org-scoped).
- Admin UI in dash0:
  - "Updates" panel on the incident detail page (`incidents.$incidentUid.tsx`) to quickly add
    updates scoped to that incident.
  - Standalone "Status updates" list page at `status-updates.tsx` for maintenance notices, FYIs,
    and any org-wide view.
- Audit events emitted to the existing `events` table on create/update/delete.

### Honest opinion

The status-page-scoped FK (required `status_page_uid`) is the critical design decision. It allows
an org with multiple status pages (internal vs customer) to publish different narratives to each
audience. Making it required keeps the data model clean — every update has exactly one public
render target. The optional `section_uid` and `check_uid` scopes let operators say "this update
is specifically about the Payment section" or "about the Stripe check" without forcing them to
model that every time. The `incident_uid` FK links automatic outage records to human narrative.

## Non-goals

- Public timeline rendering (Spec 03).
- Renaming `incidents` → `outages` (the backlog spec).
- Draft / future-scheduled delivery — `published_at` supports back-dating and future-dating but
  publication is instant with no drafts queue.
- i18n translation of update content.
- Pagination of the update feed (limit + offset is enough for v1).
- Soft-delete UX in dash0 (API DELETE is sufficient; no recycle-bin UI).

## Design

### Data model (`server/internal/db/models/status_update.go`)

```go
package models

import (
    "time"
    "github.com/google/uuid"
)

type StatusUpdateKind string

const (
    StatusUpdateKindInvestigating StatusUpdateKind = "investigating"
    StatusUpdateKindIdentified    StatusUpdateKind = "identified"
    StatusUpdateKindMonitoring    StatusUpdateKind = "monitoring"
    StatusUpdateKindResolved      StatusUpdateKind = "resolved"
    StatusUpdateKindMaintenance   StatusUpdateKind = "maintenance"
    StatusUpdateKindInfo          StatusUpdateKind = "info"
)

var validStatusUpdateKinds = map[StatusUpdateKind]struct{}{
    StatusUpdateKindInvestigating: {}, StatusUpdateKindIdentified: {},
    StatusUpdateKindMonitoring: {}, StatusUpdateKindResolved: {},
    StatusUpdateKindMaintenance: {}, StatusUpdateKindInfo: {},
}

func (k StatusUpdateKind) IsValid() bool {
    _, ok := validStatusUpdateKinds[k]
    return ok
}

type StatusUpdate struct {
    UID             string           `bun:"uid,pk,type:varchar(36)"`
    OrganizationUID string           `bun:"organization_uid,notnull"`
    StatusPageUID   string           `bun:"status_page_uid,notnull"`
    SectionUID      *string          `bun:"section_uid"`          // optional: scope to section
    CheckUID        *string          `bun:"check_uid"`            // optional: scope to check
    IncidentUID     *string          `bun:"incident_uid"`         // optional: thread under incident
    Title           string           `bun:"title,notnull"`
    BodyMarkdown    string           `bun:"body_markdown,notnull"`
    LinkURL         *string          `bun:"link_url"`
    Kind            StatusUpdateKind `bun:"kind,notnull"`
    PublishedAt     time.Time        `bun:"published_at,notnull,default:current_timestamp"`
    AuthorUID       string           `bun:"author_uid,notnull"`
    CreatedAt       time.Time        `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt       time.Time        `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt       *time.Time       `bun:"deleted_at"`
}

func NewStatusUpdate(orgUID, statusPageUID, authorUID string) *StatusUpdate {
    return &StatusUpdate{
        UID:             uuid.New().String(),
        OrganizationUID: orgUID,
        StatusPageUID:   statusPageUID,
        AuthorUID:       authorUID,
        PublishedAt:     time.Now(),
        CreatedAt:       time.Now(),
        UpdatedAt:       time.Now(),
    }
}
```

### Migration (024)

`server/internal/db/sqlite/migrations/024_status_updates.up.sql` and the matching Postgres file
(identical SQL; both dialects support this syntax):

```sql
CREATE TABLE status_updates (
    uid              VARCHAR(36)  PRIMARY KEY,
    organization_uid VARCHAR(36)  NOT NULL REFERENCES organizations(uid),
    status_page_uid  VARCHAR(36)  NOT NULL REFERENCES status_pages(uid),
    section_uid      VARCHAR(36)  REFERENCES status_page_sections(uid),
    check_uid        VARCHAR(36)  REFERENCES checks(uid),
    incident_uid     VARCHAR(36)  REFERENCES incidents(uid),
    title            TEXT         NOT NULL,
    body_markdown    TEXT         NOT NULL,
    link_url         TEXT,
    kind             TEXT         NOT NULL
                                  CHECK (kind IN (
                                      'investigating','identified','monitoring',
                                      'resolved','maintenance','info'
                                  )),
    published_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    author_uid       VARCHAR(36)  NOT NULL REFERENCES users(uid),
    created_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at       TIMESTAMP
);

CREATE INDEX idx_status_updates_org_page_pub
    ON status_updates(organization_uid, status_page_uid, published_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_status_updates_incident
    ON status_updates(incident_uid)
    WHERE incident_uid IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX idx_status_updates_check
    ON status_updates(check_uid)
    WHERE check_uid IS NOT NULL AND deleted_at IS NULL;
```

`024_status_updates.down.sql`:

```sql
DROP TABLE IF EXISTS status_updates;
```

**Note**: SQLite does not support partial indexes (`WHERE` clause). Omit those clauses in the
SQLite migration; keep them in the Postgres migration. Look at how `023_incident_notifications`
handles this split if a precedent is needed.

### Service (`server/internal/handlers/statusupdates/service.go`)

Handler pattern mirrors `server/internal/handlers/incidents/service.go`. Key validation invariants:

1. `kind` must be one of the six valid values (use `StatusUpdateKind.IsValid()`).
2. `title` must be non-empty, ≤ 200 chars.
3. `bodyMarkdown` must be non-empty, ≤ 16 384 chars.
4. `linkUrl` if set must parse as an `http` or `https` URL.
5. `statusPageUID` must exist and belong to `orgUID`.
6. `sectionUID` if set must be a section of `statusPageUID`.
7. `checkUID` if set must be a resource (`status_page_resources` row) on `statusPageUID`.
8. `incidentUID` if set: when `checkUID` is also set, `incidents.check_uid` must match;
   when `checkUID` is absent, auto-fill `checkUID` from `incidents.check_uid` (so the update is
   automatically scoped to the right check).

List query: filter by `statusPageUID`, optional `sectionUID`, `checkUID`, `incidentUID`;
order by `published_at DESC`; default `limit=50`, max `limit=200`.

Emit audit events to `events` table (see `EventType` in `server/internal/db/models/event.go`):

```go
const (
    EventTypeStatusUpdateCreated EventType = "status_update.created"
    EventTypeStatusUpdateUpdated EventType = "status_update.updated"
    EventTypeStatusUpdateDeleted EventType = "status_update.deleted"
)
```

Add these three constants to `server/internal/db/models/event.go` alongside the existing
`EventTypeIncident*` block.

### API endpoints

Register under the existing authenticated org group in `server/internal/app/server.go` (near the
`incidents` block, around line 760):

```
GET    /api/v1/orgs/:org/status-updates
       ?statusPage=<uid>&section=<uid>&check=<uid>&incident=<uid>&limit=&offset=
       → { "data": [StatusUpdateResponse] }

POST   /api/v1/orgs/:org/status-updates
       body: CreateStatusUpdateRequest
       → 201 StatusUpdateResponse

GET    /api/v1/orgs/:org/status-updates/:uid
       → StatusUpdateResponse

PATCH  /api/v1/orgs/:org/status-updates/:uid
       body: UpdateStatusUpdateRequest (all fields optional)
       → 200 StatusUpdateResponse

DELETE /api/v1/orgs/:org/status-updates/:uid
       → 204 No Content  (soft delete)
```

Response shape (mirrors `StatusPageResponse` style):

```go
type StatusUpdateResponse struct {
    UID           string    `json:"uid"`
    StatusPageUID string    `json:"statusPageUid"`
    SectionUID    *string   `json:"sectionUid,omitempty"`
    CheckUID      *string   `json:"checkUid,omitempty"`
    IncidentUID   *string   `json:"incidentUid,omitempty"`
    Title         string    `json:"title"`
    BodyMarkdown  string    `json:"bodyMarkdown"`
    LinkURL       *string   `json:"linkUrl,omitempty"`
    Kind          string    `json:"kind"`
    PublishedAt   time.Time `json:"publishedAt"`
    AuthorUID     string    `json:"authorUid"`
    CreatedAt     time.Time `json:"createdAt"`
    UpdatedAt     time.Time `json:"updatedAt"`
}
```

Error codes: `VALIDATION_ERROR` (400) for invalid kind / URL / length; `NOT_FOUND` (404) for
unknown statusPageUID, sectionUID, checkUID, incidentUID; `FORBIDDEN` (403) when the
status page belongs to a different org.

### Admin UI in dash0

#### Updates panel on incident detail (`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`)

Add a collapsible "Status updates" card above the existing timeline card (currently around
line 416). The card shows existing updates for this incident (fetched via
`useStatusUpdates({ incidentUid })`) and an "+ Add update" button that opens a dialog.

Dialog form fields (reuse design-reference dialog + form primitives):
- **Status page** — select from org's status pages (required; pre-filled to the default page if
  one exists)
- **Kind** — select with the six options; default to `investigating` for active incidents,
  `info` for resolved
- **Title** — text input, ≤ 200 chars
- **Body** — markdown textarea (plain `<textarea>`, no live preview needed in v1)
- **Link URL** — optional text input
- **Published at** — datetime picker; default = now

On submit: `useCreateStatusUpdate`. On success: invalidate the updates query and close the dialog.

Existing updates list: each row shows kind badge (colored span), timestamp (`formatDistance`),
title, and two ghost icon buttons (`Pencil` to PATCH, `Trash2` to DELETE with confirm). No
pagination in v1 (limit=50 covers most incidents).

#### Standalone updates page (`web/dash0/src/routes/orgs/$org/status-updates.tsx`)

New route accessible from the nav sidebar (add a "Status updates" link next to "Status pages").
Shows the org-wide list with filter chips: by status page (multi-select), by kind. "+ New update"
CTA in the top-right (same dialog, but status page is required and no incident pre-fill).

#### API hooks (`web/dash0/src/api/hooks.ts`)

Add:

```ts
export function useStatusUpdates(params: {
    statusPage?: string; section?: string; check?: string; incident?: string;
    limit?: number; offset?: number;
}) { ... }

export function useCreateStatusUpdate() { ... }
export function useUpdateStatusUpdate() { ... }
export function useDeleteStatusUpdate() { ... }
```

Follow the existing patterns for `useIncidents`, `useAcknowledgeIncident`, etc.

## Files to change

### New files

- `server/internal/db/models/status_update.go` — model + kind constants
- `server/internal/db/sqlite/migrations/024_status_updates.up.sql`
- `server/internal/db/sqlite/migrations/024_status_updates.down.sql`
- `server/internal/db/postgres/migrations/024_status_updates.up.sql`
- `server/internal/db/postgres/migrations/024_status_updates.down.sql`
- `server/internal/handlers/statusupdates/handler.go`
- `server/internal/handlers/statusupdates/service.go`
- `server/internal/handlers/statusupdates/handler_test.go`
- `server/internal/handlers/statusupdates/service_test.go`
- `web/dash0/src/routes/orgs/$org/status-updates.tsx`

### Modified files

- `server/internal/db/models/event.go` — add `EventTypeStatusUpdate{Created,Updated,Deleted}`
- `server/internal/app/server.go` — register routes + inject service into handler
- `server/internal/app/services/` — register `StatusUpdatesService` in the service registry
- `web/dash0/src/api/hooks.ts` — four new hooks
- `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx` — updates panel + dialog
- `web/dash0/src/components/layout/` (nav file) — add "Status updates" sidebar link

## Verification

**Backend:**

```bash
make migrate   # confirm 024 runs clean on both SQLite and Postgres
make build
make lint
make test      # new service_test.go and handler_test.go cover:
               #   - CRUD round-trip
               #   - kind validation (invalid kind → 400)
               #   - section/check FK validation
               #   - incident auto-fill of check_uid
               #   - cross-org access → 403
               #   - soft delete → 404 on subsequent GET
```

**Manual API smoke test** (run after `make dev`):

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

SP_UID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/status-pages' | jq -r '.data[0].uid')

# Create
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"statusPageUid\":\"$SP_UID\",\"kind\":\"maintenance\",\
       \"title\":\"Scheduled DB migration\",\
       \"bodyMarkdown\":\"We will run a zero-downtime migration at 02:00 UTC.\",\
       \"publishedAt\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}" \
  'http://localhost:4000/api/v1/orgs/default/status-updates' | jq .
```

**Playwright (dash0):**

- Operator navigates to `Status updates` page → list is empty.
- Clicks "+ New update", fills in all fields, submits → update appears in list with correct kind
  badge.
- Navigates to an incident detail → "Status updates" panel shows the add button → adds an
  incident-linked update → it appears in the panel.

## Risk log

| Risk | Mitigation |
|---|---|
| SQLite partial index syntax unsupported | Strip `WHERE` clauses from SQLite migration; add only in Postgres. Mirror how existing split migrations handle dialect differences. |
| `incidentUID` → `checkUID` auto-fill silently wrong if incident belongs to different org | Validate `incidents.organization_uid == orgUID` in service before deriving `checkUID`. |
| `bodyMarkdown` content too large → large DB rows | Enforce ≤ 16 384 chars in service validation before insert. |
| Section/check FK validation adds N+1 queries | Run both in the same transaction as the insert; single round-trip. |
