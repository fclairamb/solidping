# Check Groups

## Context

SolidPing checks can be organized with labels (key:value pairs), but there's no way to group them visually. Check groups provide a simple, flat grouping mechanism similar to BetterStack's monitor groups. A check belongs to zero or one group. Groups are purely organizational — for display and filtering in the dashboard.

Check groups are managed **directly within the checks page** — not as a separate section. Users can create a group alongside creating a check, and the checks list displays groups as collapsible sections (like BetterStack's monitor list).

Design decisions:
- **Flat** — no nested groups, just named containers
- **Optional** — checks don't have to belong to a group
- **One group per check** — simpler than many-to-many, covers the common case
- **Inline management** — groups are created, renamed, and deleted from the checks list page (no separate page)
- **ON DELETE SET NULL** — deleting a group un-groups its checks (no cascade delete of checks)
- **Slug-based identification** — groups support UID or slug lookups, like checks and status pages

---

## 1. Database Schema

### New migration files
- `back/internal/db/postgres/migrations/YYYYMMDD000001_check_groups.up.sql`
- `back/internal/db/postgres/migrations/YYYYMMDD000001_check_groups.down.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000001_check_groups.up.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000001_check_groups.down.sql`

### Tables

**`check_groups`** (org-scoped, soft delete)

| Column | Type | Notes |
|--------|------|-------|
| uid | PK | UUID v4 |
| organization_uid | FK organizations | NOT NULL, CASCADE |
| name | text | NOT NULL |
| slug | text | NOT NULL, 3-40 chars, unique per org |
| description | text | nullable |
| sort_order | smallint | NOT NULL, default 0 |
| created_at, updated_at, deleted_at | timestamps | standard pattern |

Indexes:
- unique `(organization_uid, slug) where deleted_at is null`
- index `(organization_uid) where deleted_at is null`

**Alter `checks` table:**
- Add `check_group_uid uuid references check_groups(uid) on delete set null` (nullable)
- Index `(check_group_uid) where check_group_uid is not null and deleted_at is null`

---

## 2. Backend Models

**New file:** `back/internal/db/models/check_group.go`

- `CheckGroup` struct: UID, OrganizationUID, Name, Slug, Description, SortOrder, CreatedAt, UpdatedAt, DeletedAt
- `NewCheckGroup(orgUID, name, slug)` constructor
- `CheckGroupUpdate` struct: Name, Slug, Description, SortOrder (all pointer fields)

**Modify:** `back/internal/db/models/check.go`

- Add `CheckGroupUID *string` to `Check` struct
- Add `CheckGroupUID *string` to `CheckUpdate` struct (nil = no change, empty string = clear, non-empty = set)

---

## 3. db.Service Interface

**File:** `back/internal/db/service.go` — add methods:

```
CreateCheckGroup, GetCheckGroup, GetCheckGroupBySlug, GetCheckGroupByUidOrSlug,
ListCheckGroups, UpdateCheckGroup, DeleteCheckGroup
```

`ListCheckGroups` orders by `sort_order ASC, name ASC`.

Implement in both:
- `back/internal/db/postgres/postgres.go`
- `back/internal/db/sqlite/sqlite.go`

Also modify:
- `ListChecks` to support `?checkGroupUid=xxx` filter
- `UpdateCheck` to handle `CheckGroupUID` field

---

## 4. Error Codes

**File:** `back/internal/handlers/base/base.go` — add:
- `ErrorCodeCheckGroupNotFound = "CHECK_GROUP_NOT_FOUND"`

---

## 5. API Endpoints

### Check Groups (authenticated, org-scoped)

| Method | Path | Handler |
|--------|------|---------|
| GET | `/api/v1/orgs/:org/check-groups` | ListCheckGroups |
| POST | `/api/v1/orgs/:org/check-groups` | CreateCheckGroup |
| GET | `/api/v1/orgs/:org/check-groups/:uid` | GetCheckGroup |
| PATCH | `/api/v1/orgs/:org/check-groups/:uid` | UpdateCheckGroup |
| DELETE | `/api/v1/orgs/:org/check-groups/:uid` | DeleteCheckGroup |

### Response shapes

**CheckGroup:**
```json
{
  "uid": "...",
  "name": "API Services",
  "slug": "api-services",
  "description": "All API-related checks",
  "sortOrder": 0,
  "checkCount": 5,
  "createdAt": "...",
  "updatedAt": "..."
}
```

**List response:** `{ "data": [...] }`

**Create request:**
```json
{
  "name": "API Services",
  "slug": "api-services",
  "description": "All API-related checks",
  "sortOrder": 0
}
```

**Update request (PATCH):**
```json
{
  "name": "Backend Services",
  "description": "Updated description",
  "sortOrder": 1
}
```

The `checkCount` field is computed via a LEFT JOIN COUNT on the checks table.

### Changes to existing Check endpoints

- `CreateCheckRequest`: add `checkGroupUid` (optional string)
- `UpdateCheckRequest`: add `checkGroupUid` (optional string, set to `null` in JSON to un-group)
- `UpsertCheckRequest`: add `checkGroupUid` (optional string)
- `CheckResponse`: add `checkGroupUid` (optional string)
- `ListChecks`: support `?checkGroupUid=xxx` query parameter for filtering. Special value `none` filters for ungrouped checks (`check_group_uid IS NULL`).

---

## 6. Backend Handler Package

**New directory:** `back/internal/handlers/checkgroups/`

- `service.go` — business logic, request/response types, slug validation
- `handler.go` — HTTP handlers (same pattern as `checks/handler.go`)

**Route registration** in `back/internal/app/server.go`:
- Add `checkgroups` import
- Create service + handler
- Register routes under org-scoped group with auth middleware

---

## 7. Frontend — dash0

### API Types & Hooks
**File:** `apps/dash0/src/api/hooks.ts` — add:
- `CheckGroup` interface: `{ uid, name, slug, description?, sortOrder, checkCount, createdAt, updatedAt }`
- Add `checkGroupUid?: string` to `Check`, `CreateCheckRequest`, `UpdateCheckRequest`
- Query hooks: `useCheckGroups`, `useCheckGroup`
- Mutation hooks: `useCreateCheckGroup`, `useUpdateCheckGroup`, `useDeleteCheckGroup`

### Check List Page — grouped display with per-group infinite scrolling
**File:** `apps/dash0/src/routes/orgs/$org/checks.index.tsx`

The checks list page becomes the single place to manage both checks and groups (like BetterStack's monitor list — see screenshot).

**Data loading strategy:**
1. On page load, fetch all check groups via `useCheckGroups(org)` (single request, groups are few).
2. For each group, render a `<CheckGroupSection>` component that independently fetches its checks using `useInfiniteChecks(org, { checkGroupUid: group.uid, ... })`.
3. A final section at the bottom fetches ungrouped checks using `useInfiniteChecks(org, { checkGroupUid: "none", ... })` (or similar sentinel).
4. Each section has its own infinite scroll sentinel — scrolling within an expanded group loads more checks for that group only.

**Layout (matching BetterStack):**
- Each group is a collapsible card with a chevron toggle (▶ collapsed / ▼ expanded).
- **Collapsed**: Shows group name and check count only.
- **Expanded**: Shows the group name header + the checks table with per-group infinite scrolling.
- Groups are rendered in `sort_order ASC` order.
- Ungrouped checks appear at the bottom.

**Group headers**: Each group header shows:
- Chevron toggle (collapse/expand)
- Group name
- Check count
- Actions menu (rename, delete, move up/down)

**"New Group" button**: Next to the existing "New Check" button, add a "New Group" button that opens a simple dialog/popover to name the group.

**Reordering groups**: Groups can be reordered via "Move up" / "Move down" actions in the group header menu. When a group's `sortOrder` is changed via PATCH, the **backend** normalizes all groups in the same organization: it reorders them by the new sort values and reassigns `sortOrder` with a gap of 2 between each group (0, 2, 4, 6, ...). This means every PATCH to `sortOrder` updates all groups in the org, keeping the values clean and evenly spaced.

Example: groups A(0), B(2), C(4) — moving C above B means PATCHing C with `sortOrder: 1`. The backend sees the new order A(0), C(1), B(2) and normalizes to A(0), C(2), B(4).

**Search**: The existing search input filters checks across all groups. When a search query is active, all groups are expanded and each group's `useInfiniteChecks` passes the `q` parameter.

### Check Form — group selector
**File:** `apps/dash0/src/components/shared/check-form.tsx`
- Add a "Group" combobox/select listing available groups + "No group" option
- Include a "Create new group" option inline in the dropdown (type a name, press enter to create)
- Pass `checkGroupUid` in create/update requests

### No separate check-groups page
Groups are managed entirely from the checks list. No sidebar entry or dedicated route needed.

---

## 8. Implementation Order

1. **Migrations** — new migration files (PostgreSQL + SQLite, up + down)
2. **Models** — `check_group.go` + modify `check.go`
3. **db.Service interface** — add method signatures
4. **DB implementations** — PostgreSQL + SQLite
5. **Error codes** — add to `base.go`
6. **Handler package** — `checkgroups/service.go` + `checkgroups/handler.go`
7. **Route registration** — update `server.go`
8. **Modify checks handler** — add `checkGroupUid` to create/update/list/response
9. **Verify backend** — `make build` + curl test
10. **dash0: Frontend hooks** — types + API hooks for check groups
11. **dash0: Check form** — add group selector with inline "create new group"
12. **dash0: Check list** — grouped display with collapsible sections, group filter, group actions
13. **Verify end-to-end** — full flow testing

---

## 9. Key Files

| File | Change |
|------|--------|
| `back/internal/db/postgres/migrations/..._check_groups.up.sql` | **New** — migration |
| `back/internal/db/sqlite/migrations/..._check_groups.up.sql` | **New** — migration |
| `back/internal/db/models/check_group.go` | **New** — CheckGroup model |
| `back/internal/db/models/check.go` | Add `CheckGroupUID` field |
| `back/internal/db/service.go` | Add CheckGroup interface methods |
| `back/internal/db/postgres/postgres.go` | Implement CheckGroup operations |
| `back/internal/db/sqlite/sqlite.go` | Implement CheckGroup operations |
| `back/internal/handlers/base/base.go` | Add `CHECK_GROUP_NOT_FOUND` |
| `back/internal/handlers/checkgroups/service.go` | **New** — business logic |
| `back/internal/handlers/checkgroups/handler.go` | **New** — HTTP handlers |
| `back/internal/handlers/checks/service.go` | Add `checkGroupUid` to request/response types |
| `back/internal/handlers/checks/handler.go` | Parse `checkGroupUid` query param |
| `back/internal/app/server.go` | Register check group routes |
| `apps/dash0/src/api/hooks.ts` | Add CheckGroup types + hooks |
| `apps/dash0/src/components/shared/check-form.tsx` | Add group selector |
| `apps/dash0/src/routes/orgs/$org/checks.index.tsx` | Grouped display, group filter, inline group management |

---

## 10. Verification

### Backend
```bash
make build
SP_DB_RESET=true make dev-backend

TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create a check group
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"API Services","slug":"api-services"}' \
  'http://localhost:4000/api/v1/orgs/default/check-groups'

# List check groups
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/check-groups'

# Create a check in the group
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"http","name":"API Health","config":{"url":"https://example.com"},"checkGroupUid":"GROUP_UID"}' \
  'http://localhost:4000/api/v1/orgs/default/checks'

# Filter checks by group
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks?checkGroupUid=GROUP_UID'
```

### Frontend
```bash
make dev-dash0
# Navigate to /orgs/default/checks:
#   - Click "New Group" to create a group inline
#   - Verify checks display in collapsible group sections
#   - Use group filter dropdown to filter by group
#   - Rename/delete a group from its header actions menu
# Create/edit a check — verify group selector with inline "create new group"
```

### Tests
```bash
make test
make lint
```
