# Status Pages with Sections

## Context

SolidPing needs status pages (similar to BetterStack) so users can publish the health of their services. A status page groups checks into named sections, each displayed publicly. This adds three new entities: **StatusPage**, **StatusPageSection**, and **StatusPageResource** (a check assigned to a section).

Design decisions:
- Status page detail returns sections inline by default, with `?with=sections` opt-in
- A **public** (unauthenticated) read-only API endpoint for viewing status pages
- Resources include **live check status** (status, lastResult) in responses
- Each org has a **default status page** — visiting the org's public URL redirects to it
- Public rendering is handled by a separate lightweight app **`status0`** (like `dash0`)

---

## 1. Database Schema

### Migration: append to existing `20251207000001_initial` files

**Files to modify:**
- `back/internal/db/sqlite/migrations/20251207000001_initial.up.sql` — append tables at the end
- `back/internal/db/sqlite/migrations/20251207000001_initial.down.sql` — add drop statements at the top
- `back/internal/db/postgres/migrations/20251207000001_initial.up.sql` — append tables at the end
- `back/internal/db/postgres/migrations/20251207000001_initial.down.sql` — add drop statements at the top

### Tables

**`status_pages`** (org-scoped, soft delete)

| Column | Type | Notes |
|--------|------|-------|
| uid | PK | UUID v4 |
| organization_uid | FK organizations | NOT NULL, CASCADE |
| name | text | NOT NULL |
| slug | text | NOT NULL, 3-40 chars, unique per org |
| description | text | nullable |
| visibility | text | 'public' or 'private', default 'public' |
| is_default | bool | default false, at most one per org |
| enabled | bool | default true |
| created_at, updated_at, deleted_at | timestamps | standard pattern |

Indexes:
- unique `(organization_uid, slug) where deleted_at is null`
- unique partial `(organization_uid) where is_default = true and deleted_at is null` — enforces at most one default per org

**`status_page_sections`** (soft delete)

| Column | Type | Notes |
|--------|------|-------|
| uid | PK | UUID v4 |
| status_page_uid | FK status_pages | NOT NULL, CASCADE |
| name | text | NOT NULL |
| slug | text | NOT NULL, 3-40 chars, unique per page |
| position | int | NOT NULL, default 0 |
| created_at, updated_at, deleted_at | timestamps | |

Indexes:
- unique `(status_page_uid, slug) where deleted_at is null`
- index on `status_page_uid where deleted_at is null`

**`status_page_resources`** (hard delete, like check_connections)

| Column | Type | Notes |
|--------|------|-------|
| uid | PK | UUID v4 |
| section_uid | FK status_page_sections | NOT NULL, CASCADE |
| check_uid | FK checks | NOT NULL, CASCADE |
| public_name | text | nullable, display override |
| explanation | text | nullable |
| position | int | NOT NULL, default 0 |
| created_at, updated_at | timestamps | no deleted_at |

Indexes:
- unique `(section_uid, check_uid)`
- index on `check_uid`

---

## 2. Backend Models

**New file:** `back/internal/db/models/status_page.go`

Three structs following `check.go` patterns:
- `StatusPage` + `NewStatusPage()` + `StatusPageUpdate` — includes `IsDefault bool`
- `StatusPageSection` + `NewStatusPageSection()` + `StatusPageSectionUpdate`
- `StatusPageResource` + `NewStatusPageResource()` + `StatusPageResourceUpdate`

---

## 3. db.Service Interface

**File:** `back/internal/db/service.go` — add methods:

```
// StatusPage operations
CreateStatusPage, GetStatusPage, GetStatusPageBySlug, GetStatusPageByUidOrSlug,
GetDefaultStatusPage (by orgUID), ListStatusPages, UpdateStatusPage, DeleteStatusPage

// StatusPageSection operations
CreateStatusPageSection, GetStatusPageSection, GetStatusPageSectionBySlug,
ListStatusPageSections, UpdateStatusPageSection, DeleteStatusPageSection

// StatusPageResource operations
CreateStatusPageResource, GetStatusPageResource,
ListStatusPageResources, UpdateStatusPageResource, DeleteStatusPageResource
```

Implement in both:
- `back/internal/db/sqlite/sqlite.go`
- `back/internal/db/postgres/postgres.go`

Pattern reference: existing check operations in those files.

**Default status page logic:** When setting `is_default=true`, the service layer clears `is_default` on all other pages in the same org first (same transaction). The first status page created for an org is automatically set as default.

---

## 4. Error Codes

**File:** `back/internal/handlers/base/base.go` — add:
- `ErrorCodeStatusPageNotFound = "STATUS_PAGE_NOT_FOUND"`
- `ErrorCodeStatusPageSectionNotFound = "STATUS_PAGE_SECTION_NOT_FOUND"`

---

## 5. API Endpoints

### Management (authenticated, org-scoped)

| Method | Path | Handler |
|--------|------|---------|
| GET | `/api/v1/orgs/:org/status-pages` | ListStatusPages |
| POST | `/api/v1/orgs/:org/status-pages` | CreateStatusPage |
| GET | `/api/v1/orgs/:org/status-pages/:statusPageUid` | GetStatusPage |
| PATCH | `/api/v1/orgs/:org/status-pages/:statusPageUid` | UpdateStatusPage |
| DELETE | `/api/v1/orgs/:org/status-pages/:statusPageUid` | DeleteStatusPage |
| GET | `…/:statusPageUid/sections` | ListSections |
| POST | `…/:statusPageUid/sections` | CreateSection |
| GET | `…/:statusPageUid/sections/:sectionUid` | GetSection |
| PATCH | `…/:statusPageUid/sections/:sectionUid` | UpdateSection |
| DELETE | `…/:statusPageUid/sections/:sectionUid` | DeleteSection |
| GET | `…/:sectionUid/resources` | ListResources |
| POST | `…/:sectionUid/resources` | CreateResource |
| PATCH | `…/:sectionUid/resources/:resourceUid` | UpdateResource |
| DELETE | `…/:sectionUid/resources/:resourceUid` | DeleteResource |

### Public (unauthenticated)

| Method | Path | Handler |
|--------|------|---------|
| GET | `/api/v1/status-pages/:org/:slug` | ViewStatusPage |
| GET | `/api/v1/status-pages/:org` | ViewDefaultStatusPage (returns default page) |

The public endpoints return the full status page with sections, resources, and **live check status** (status + lastResult for each resource). Only works for `enabled=true` and `visibility=public` pages.

---

## 6. Backend Handler Package

**New directory:** `back/internal/handlers/statuspages/`

- `service.go` — business logic, request/response types, slug validation (reuse `validateSlug` pattern from checks service)
- `handler.go` — HTTP handlers (same pattern as `checks/handler.go`)

**Response shapes:**
- StatusPage: `{uid, name, slug, description, visibility, isDefault, enabled, sections?, createdAt}`
- Section: `{uid, name, slug, position, resources?, createdAt}`
- Resource: `{uid, checkUid, publicName, explanation, position, check?, createdAt}` — `check` includes `{name, type, status, lastResult}` for live data

**Route registration** in `back/internal/app/server.go`:
- Add `statuspages` import
- Create service + handler
- Register all management routes under org-scoped group with auth middleware
- Register public ViewStatusPage and ViewDefaultStatusPage endpoints without auth

---

## 7. Frontend — Management (dash0)

### API Types & Hooks
**File:** `apps/dash0/src/api/hooks.ts` — add:
- `StatusPage`, `StatusPageSection`, `StatusPageResource` interfaces
- Query hooks: `useStatusPages`, `useStatusPage`, `useStatusPageSections`, `useStatusPageResources`
- Mutation hooks: `useCreateStatusPage`, `useUpdateStatusPage`, `useDeleteStatusPage`, plus section/resource equivalents

### Sidebar
**File:** `apps/dash0/src/components/layout/AppSidebar.tsx`
- Add `Globe` icon import from lucide-react
- Add entry after Events: `{ title: "Status Pages", path: "/orgs/$org/status-pages", icon: Globe }`

### Route Files (new, all under `apps/dash0/src/routes/orgs/$org/`)
| File | Purpose |
|------|---------|
| `status-pages.tsx` | Layout with `<Outlet />` |
| `status-pages.index.tsx` | List page (table with name, slug, visibility, isDefault badge, enabled) |
| `status-pages.new.tsx` | Create form |
| `status-pages.$statusPageUid.tsx` | Detail layout |
| `status-pages.$statusPageUid.index.tsx` | Detail: shows sections with inline resources, add/remove/reorder |
| `status-pages.$statusPageUid.edit.tsx` | Edit form |

### Shared Component
**File:** `apps/dash0/src/components/shared/status-page-form.tsx`
- Fields: name, slug (auto-generated from name), description, visibility select, isDefault toggle, enabled toggle
- Reused by create and edit pages (mode prop)

---

## 8. Frontend — Public Rendering (status0)

A new lightweight app at `apps/status0/`, separate from dash0, following the same tech stack and architecture as dash0.

### Why separate
- Public-facing, no auth needed — much simpler than dash0
- Lighter bundle (no admin UI components, forms, dialogs)
- Can be styled/themed independently for end-user branding

### Tech Stack (same as dash0)
- React 19 + TypeScript + Vite 7 + Bun
- TanStack Router (file-based) + TanStack Query
- Tailwind CSS v4 + Radix UI + Lucide icons
- Recharts for timeline

### Served at `/status0/` (same pattern as `/dash0/`)

### Backend integration
- Embedded via `go:embed status0res/*` in `back/internal/app/server.go`
- Served at `/status0/` and `/status0/*path` with SPA fallback
- Dev proxy: `SP_REDIRECTS="/status0:localhost:5175/status0"`

### Makefile additions
- `build-status0` — build the status0 app
- `copy-status0` — copy dist to `back/internal/app/status0res/`
- `dev-status0` — start dev server (port 5175)
- Update `build` target to include status0
- Update `deps` and `clean` targets

### App structure
```
apps/status0/
├── src/
│   ├── api/
│   │   ├── client.ts          # Minimal fetch wrapper (no auth)
│   │   └── hooks.ts           # useStatusPage(org, slug) hook
│   ├── components/
│   │   ├── shared/
│   │   │   ├── status-header.tsx      # Page name, description, overall status
│   │   │   ├── status-section.tsx     # Section with its resources
│   │   │   └── status-resource.tsx    # Single resource with status + timeline
│   │   └── ui/                # Minimal set: badge, card, skeleton
│   ├── routes/
│   │   ├── __root.tsx         # Root layout (minimal, no sidebar)
│   │   ├── $org.tsx           # Org layout — fetches default status page
│   │   └── $org.$slug.tsx     # Renders a specific status page
│   ├── main.tsx
│   └── index.css
├── vite.config.ts             # base: "/status0/", port: 5175, proxy /api to :4000
├── package.json
├── tsconfig.json
└── index.html
```

### Routes
- `/status0/:org` — shows the default status page for the org
- `/status0/:org/:slug` — shows a specific status page

### Features (v1)
- Overall status banner (all operational / partial outage / major outage)
- Sections displayed in position order
- Each resource shows: name, current status badge (operational/down/degraded), explanation
- Status timeline per resource (48 data points, similar to dash0)
- Auto-refresh every 30 seconds
- Light/dark mode support
- Responsive design

---

## 9. Implementation Order

1. **Migrations** — append to existing `20251207000001_initial` files (both SQLite + Postgres up/down)
2. **Models** — `back/internal/db/models/status_page.go`
3. **db.Service interface** — add method signatures
4. **DB implementations** — SQLite + PostgreSQL (both files)
5. **Error codes** — add to `base.go`
6. **Handler package** — `statuspages/service.go` + `statuspages/handler.go`
7. **Route registration** — update `server.go` (management + public + status0 static serving)
8. **Verify backend** — `make build` + curl test
9. **dash0: Frontend hooks** — types + API hooks in `hooks.ts`
10. **dash0: Sidebar** — add nav entry
11. **dash0: Route files** — all 6 route files + form component
12. **status0: Scaffold app** — copy dash0 structure, strip to minimal
13. **status0: API + components** — client, hooks, status page rendering components
14. **status0: Makefile** — add build/dev/copy targets
15. **Verify end-to-end** — management in dash0, public view in status0

---

## 10. Verification

### Backend
```bash
make build
SP_DB_RESET=true make dev-backend

# Create a status page (auto-default)
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Production","slug":"production"}' \
  'http://localhost:4000/api/v1/orgs/default/status-pages'

# Public endpoints
curl -s 'http://localhost:4000/api/v1/status-pages/default/production'
curl -s 'http://localhost:4000/api/v1/status-pages/default'  # default page
```

### dash0 (management)
```bash
make dev-dash0
# Navigate to /orgs/default/status-pages — CRUD status pages, sections, resources
```

### status0 (public rendering)
```bash
make dev-status0
# Navigate to /status0/default — shows default status page
# Navigate to /status0/default/production — shows specific page
```

### Tests
```bash
make test      # backend
make lint      # linting
```
