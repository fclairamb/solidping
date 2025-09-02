# Maintenance Windows

## Context

SolidPing has no way to suppress alerts during planned downtime. Users performing scheduled maintenance (DB backups, deployments, infra upgrades) trigger false incidents and noisy notifications. This is a table-stakes feature offered by BetterStack, UptimeRobot, Pingdom, and StatusCake.

Design decisions:
- **Dedicated sidebar page** — maintenance windows are a first-class entity with list/create/detail/edit pages (like Status Pages)
- **On-the-fly recurrence** — a single row stores the recurrence rule; "is active now" is computed at query time (no background job, no pre-generated rows)
- **Check + group scope** — a maintenance window can target individual checks and/or check groups (all checks in that group are covered)
- **Data collection continues** — checks still execute during maintenance so metrics are not interrupted, but failures do NOT create incidents

---

## 1. Database Schema

### New migration files
- `back/internal/db/postgres/migrations/YYYYMMDD000001_maintenance_windows.up.sql`
- `back/internal/db/postgres/migrations/YYYYMMDD000001_maintenance_windows.down.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000001_maintenance_windows.up.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000001_maintenance_windows.down.sql`

### Tables

**`maintenance_windows`** (org-scoped, soft delete)

| Column | Type | Notes |
|--------|------|-------|
| uid | PK | UUID v4 |
| organization_uid | FK organizations | NOT NULL, CASCADE |
| title | text | NOT NULL |
| description | text | nullable |
| start_at | timestamptz | NOT NULL, window start (UTC) |
| end_at | timestamptz | NOT NULL, window end (UTC) |
| recurrence | text | NOT NULL, default `'none'`. Values: `none`, `daily`, `weekly`, `monthly` |
| recurrence_end | timestamptz | nullable, when recurrence stops |
| created_by | text | nullable, user UID who created it |
| created_at, updated_at, deleted_at | timestamps | standard pattern |

Constraints:
- `CHECK (end_at > start_at)`
- `CHECK (recurrence IN ('none', 'daily', 'weekly', 'monthly'))`

Indexes:
- `(organization_uid) WHERE deleted_at IS NULL`
- `(organization_uid, start_at, end_at) WHERE deleted_at IS NULL` — for active window queries

**`maintenance_window_checks`** (hard delete, like check_connections)

| Column | Type | Notes |
|--------|------|-------|
| uid | PK | UUID v4 |
| maintenance_window_uid | FK maintenance_windows | NOT NULL, CASCADE |
| check_uid | FK checks | nullable, CASCADE |
| check_group_uid | FK check_groups | nullable, CASCADE |
| created_at | timestamp | |

Constraints:
- `CHECK ((check_uid IS NOT NULL AND check_group_uid IS NULL) OR (check_uid IS NULL AND check_group_uid IS NOT NULL))` — exactly one must be set

Indexes:
- unique `(maintenance_window_uid, check_uid) WHERE check_uid IS NOT NULL`
- unique `(maintenance_window_uid, check_group_uid) WHERE check_group_uid IS NOT NULL`
- `(check_uid) WHERE check_uid IS NOT NULL`
- `(check_group_uid) WHERE check_group_uid IS NOT NULL`

---

## 2. Backend Models

**New file:** `back/internal/db/models/maintenance_window.go`

- `MaintenanceWindow` struct: UID, OrganizationUID, Title, Description, StartAt, EndAt, Recurrence, RecurrenceEnd, CreatedBy, CreatedAt, UpdatedAt, DeletedAt
- `NewMaintenanceWindow(orgUID, title, startAt, endAt)` constructor
- `MaintenanceWindowUpdate` struct: Title, Description, StartAt, EndAt, Recurrence, RecurrenceEnd (all pointer fields)
- `MaintenanceWindowCheck` struct: UID, MaintenanceWindowUID, CheckUID, CheckGroupUID, CreatedAt
- `NewMaintenanceWindowCheck(windowUID, checkUID, checkGroupUID)` constructor

**Filter struct:**
```
ListMaintenanceWindowsFilter {
    Status string           // "active", "upcoming", "past", "all" (default "all")
    Limit  int
    Cursor string
}
```

**Recurrence helper** (pure Go, used by DB layer):
```
IsActiveAt(window *MaintenanceWindow, t time.Time) bool
```
Logic:
- If `recurrence == "none"`: return `t >= start_at && t < end_at`
- If `recurrence_end != nil && t > recurrence_end`: return false
- Compute duration = `end_at - start_at`
- Based on recurrence type, compute the most recent occurrence start before `t` by stepping forward from `start_at` in daily/weekly/monthly increments
- Return `t >= occurrence_start && t < occurrence_start + duration`

---

## 3. db.Service Interface

**File:** `back/internal/db/service.go` — add methods:

```
// MaintenanceWindow operations
CreateMaintenanceWindow, GetMaintenanceWindow, ListMaintenanceWindows,
UpdateMaintenanceWindow, DeleteMaintenanceWindow

// MaintenanceWindowCheck operations
SetMaintenanceWindowChecks(ctx, windowUID string, checkUIDs []string, checkGroupUIDs []string) error
ListMaintenanceWindowChecks(ctx, windowUID string) ([]*MaintenanceWindowCheck, error)

// Active maintenance query
IsCheckInActiveMaintenance(ctx, checkUID string) (bool, error)
```

`IsCheckInActiveMaintenance` implementation:
1. Query all non-deleted maintenance windows in the check's organization that have associated entries for this check (directly via `check_uid` OR via `check_group_uid` matching the check's `check_group_uid`)
2. For each candidate window, call `IsActiveAt(window, time.Now())`
3. Return true if any match

Implement in both:
- `back/internal/db/postgres/postgres.go`
- `back/internal/db/sqlite/sqlite.go`

---

## 4. Error Codes

**File:** `back/internal/handlers/base/base.go` — add:
- `ErrorCodeMaintenanceWindowNotFound = "MAINTENANCE_WINDOW_NOT_FOUND"`

---

## 5. API Endpoints

### Maintenance Windows (authenticated, org-scoped)

| Method | Path | Handler |
|--------|------|---------|
| GET | `/api/v1/orgs/:org/maintenance-windows` | ListMaintenanceWindows |
| POST | `/api/v1/orgs/:org/maintenance-windows` | CreateMaintenanceWindow |
| GET | `/api/v1/orgs/:org/maintenance-windows/:uid` | GetMaintenanceWindow |
| PATCH | `/api/v1/orgs/:org/maintenance-windows/:uid` | UpdateMaintenanceWindow |
| DELETE | `/api/v1/orgs/:org/maintenance-windows/:uid` | DeleteMaintenanceWindow |
| GET | `…/:uid/checks` | ListMaintenanceWindowChecks |
| PUT | `…/:uid/checks` | SetMaintenanceWindowChecks |

### Query parameters (list)

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `status` | string | `all` | Filter: `active`, `upcoming`, `past`, `all` |
| `limit` | int | 20 | Max 100 |
| `cursor` | string | | Pagination cursor |

### Response shapes

**MaintenanceWindow:**
```json
{
  "uid": "...",
  "title": "Weekly DB Backup",
  "description": "Automated backup every Sunday 2-4 AM",
  "startAt": "2026-03-22T02:00:00Z",
  "endAt": "2026-03-22T04:00:00Z",
  "recurrence": "weekly",
  "recurrenceEnd": null,
  "status": "upcoming",
  "checkCount": 3,
  "checkGroupCount": 1,
  "createdBy": "user-uid",
  "createdAt": "...",
  "updatedAt": "..."
}
```

**List response:** `{ "data": [...], "pagination": { "cursor": "...", "hasMore": false } }`

**Create request:**
```json
{
  "title": "Weekly DB Backup",
  "description": "Automated backup every Sunday 2-4 AM",
  "startAt": "2026-03-22T02:00:00Z",
  "endAt": "2026-03-22T04:00:00Z",
  "recurrence": "weekly",
  "recurrenceEnd": null,
  "checkUid": ["check-uid-1", "check-uid-2"],
  "checkGroupUid": ["group-uid-1"]
}
```

**Set checks request (PUT):**
```json
{
  "checkUid": ["check-uid-1", "check-uid-2"],
  "checkGroupUid": ["group-uid-1"]
}
```

The `status` field in responses is computed, not stored: `active` if currently in an active occurrence, `upcoming` if `start_at` is in the future (or next recurrence is future), `past` otherwise.

The `checkCount` and `checkGroupCount` fields are computed via JOIN COUNT.

---

## 6. Backend Handler Package

**New directory:** `back/internal/handlers/maintenancewindows/`

- `service.go` — business logic, request/response types, validation (title required, end > start, valid recurrence value)
- `handler.go` — HTTP handlers (same pattern as `checks/handler.go`)

**Route registration** in `back/internal/app/server.go`:
- Add `maintenancewindows` import
- Create service + handler
- Register routes under org-scoped group with auth middleware

---

## 7. Incident Integration

**File:** `back/internal/handlers/incidents/service.go`

Modify `ProcessCheckResult` — add maintenance check early in the function, after the nil-status guard:

```go
// Check if this check is in an active maintenance window
inMaintenance, err := s.db.IsCheckInActiveMaintenance(ctx, check.UID)
if err != nil {
    slog.Warn("failed to check maintenance status", "checkUID", check.UID, "error", err)
    // Continue processing — don't block on maintenance check failure
}
if inMaintenance {
    // Still update check status tracking, but skip incident creation/notification
    if err := s.db.UpdateCheckStatus(ctx, check.UID, newStatus, newStreak, statusChangedAt); err != nil {
        return fmt.Errorf("failed to update check status: %w", err)
    }
    return nil
}
```

Results are still saved upstream (in the worker), so data collection is unaffected.

---

## 8. Frontend — dash0

### API Types & Hooks
**File:** `apps/dash0/src/api/hooks.ts` — add:
- `MaintenanceWindow` interface: `{ uid, title, description?, startAt, endAt, recurrence, recurrenceEnd?, status, checkCount, checkGroupCount, createdBy?, createdAt, updatedAt }`
- `MaintenanceWindowCheck` interface: `{ uid, checkUid?, checkGroupUid?, createdAt }`
- Query hooks: `useMaintenanceWindows(org, { status?, limit?, cursor? })`, `useMaintenanceWindow(org, uid)`, `useMaintenanceWindowChecks(org, uid)`
- Mutation hooks: `useCreateMaintenanceWindow`, `useUpdateMaintenanceWindow`, `useDeleteMaintenanceWindow`, `useSetMaintenanceWindowChecks`

### Sidebar
**File:** `apps/dash0/src/components/layout/AppSidebar.tsx`
- Add `Calendar` icon import from lucide-react
- Add entry after Incidents: `{ title: "Maintenance", path: "/orgs/$org/maintenance-windows", icon: Calendar }`

### Route Files (new, all under `apps/dash0/src/routes/orgs/$org/`)

| File | Purpose |
|------|---------|
| `maintenance-windows.tsx` | Layout with `<Outlet />` |
| `maintenance-windows.index.tsx` | List page |
| `maintenance-windows.new.tsx` | Create form |
| `maintenance-windows.$uid.tsx` | Detail layout |
| `maintenance-windows.$uid.index.tsx` | Detail view |
| `maintenance-windows.$uid.edit.tsx` | Edit form |

### Shared Component
**File:** `apps/dash0/src/components/shared/maintenance-window-form.tsx`
- Fields:
  - Title (text input, required) — `data-testid="mw-title-input"`
  - Description (textarea, optional) — `data-testid="mw-description-input"`
  - Start date/time (datetime-local input) — `data-testid="mw-start-input"`
  - End date/time (datetime-local input) — `data-testid="mw-end-input"`
  - Recurrence (select: None, Daily, Weekly, Monthly) — `data-testid="mw-recurrence-select"`
  - Recurrence end (datetime-local, shown only when recurrence != none) — `data-testid="mw-recurrence-end-input"`
  - Checks (multi-select from org checks) — `data-testid="mw-checks-select"`
  - Check Groups (multi-select from org check groups) — `data-testid="mw-groups-select"`
- Submit button — `data-testid="mw-submit-button"`
- Reused by create and edit pages (mode prop)
- Validation: title required, end must be after start, at least one check or group selected

### List Page (`maintenance-windows.index.tsx`)
- Page heading: "Maintenance Windows"
- "New Maintenance Window" button — `data-testid="new-mw-button"`
- Table columns: Title, Schedule (formatted start–end), Recurrence, Status badge, Checks count
- Status badges:
  - **Active** (green): currently in an active occurrence
  - **Upcoming** (blue): next occurrence is in the future
  - **Past** (gray): all occurrences finished
- Row click navigates to detail page
- Dropdown actions: Edit, Delete
- Empty state: "No maintenance windows scheduled"

### Detail Page (`maintenance-windows.$uid.index.tsx`)
- Header: title with status badge
- Description (if present)
- Schedule card: start, end, recurrence, recurrence end
- Affected checks card: list of check names and check group names with links
- Action buttons: Edit, Delete (with AlertDialog confirmation)

---

## 9. Status Page Integration

**File:** `apps/status0/src/components/shared/status-resource.tsx`

When rendering a resource's status, the backend public endpoint (`/api/v1/status-pages/:org/:slug`) should include a `inMaintenance: true` flag on each resource whose check is currently in active maintenance.

**Backend change:** In `statuspages/service.go`, when building the public response, call `IsCheckInActiveMaintenance` for each resource's check and set the flag.

**Frontend change:** In status0, when `inMaintenance` is true, render a "Scheduled Maintenance" badge (yellow/amber) instead of the normal status indicator.

---

## 10. E2E Tests

**New file:** `apps/dash0/e2e/maintenance-windows.spec.ts`

```typescript
import { test, expect } from "./fixtures";

test.describe("Maintenance Windows", () => {
  test("should display the maintenance windows list page", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // Navigate via sidebar
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Maintenance" }).click();
    await page.waitForURL(/\/maintenance-windows/);
    await page.waitForLoadState("networkidle");
    // Verify heading and empty state
    await expect(page.getByRole("heading", { name: "Maintenance Windows" })).toBeVisible();
    await expect(page.getByTestId("new-mw-button")).toBeVisible();
    // Screenshot
    await page.screenshot({ path: "test-results/screenshots/mw-list.png", fullPage: true });
  });

  test("should navigate to new maintenance window form", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Maintenance" }).click();
    await page.waitForURL(/\/maintenance-windows/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-mw-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    // Verify form elements
    await expect(page.getByTestId("mw-title-input")).toBeVisible();
    await expect(page.getByTestId("mw-start-input")).toBeVisible();
    await expect(page.getByTestId("mw-end-input")).toBeVisible();
    await expect(page.getByTestId("mw-recurrence-select")).toBeVisible();
    await expect(page.getByTestId("mw-submit-button")).toBeVisible();
    await page.screenshot({ path: "test-results/screenshots/mw-new-form.png", fullPage: true });
  });

  test("should create a one-time maintenance window", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // First create a check so we have something to associate
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    const checkName = `MW Test Check ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });

    // Now create maintenance window
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Maintenance" }).click();
    await page.waitForURL(/\/maintenance-windows/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-mw-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    const mwTitle = `E2E Maintenance ${Date.now()}`;
    await page.getByTestId("mw-title-input").fill(mwTitle);

    // Set start/end to a future window
    const now = new Date();
    const start = new Date(now.getTime() + 3600000); // +1h
    const end = new Date(now.getTime() + 7200000);   // +2h
    await page.getByTestId("mw-start-input").fill(start.toISOString().slice(0, 16));
    await page.getByTestId("mw-end-input").fill(end.toISOString().slice(0, 16));

    // Select check
    await page.getByTestId("mw-checks-select").click();
    await page.getByText(checkName).click();

    await page.getByTestId("mw-submit-button").click();

    // Wait for navigation to detail page
    await page.waitForURL(/\/maintenance-windows\/[0-9a-f]{8}-/, { timeout: 10000 });
    await expect(page.getByRole("heading", { name: mwTitle })).toBeVisible();
    await page.screenshot({ path: "test-results/screenshots/mw-created.png", fullPage: true });
  });

  test("should create a recurring weekly maintenance window", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Maintenance" }).click();
    await page.waitForURL(/\/maintenance-windows/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-mw-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("mw-title-input").fill(`E2E Recurring ${Date.now()}`);
    const now = new Date();
    await page.getByTestId("mw-start-input").fill(new Date(now.getTime() + 3600000).toISOString().slice(0, 16));
    await page.getByTestId("mw-end-input").fill(new Date(now.getTime() + 7200000).toISOString().slice(0, 16));

    // Select weekly recurrence
    await page.getByTestId("mw-recurrence-select").click();
    await page.getByRole("option", { name: "Weekly" }).click();

    // Recurrence end input should now be visible
    await expect(page.getByTestId("mw-recurrence-end-input")).toBeVisible();

    await page.getByTestId("mw-submit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.screenshot({ path: "test-results/screenshots/mw-recurring.png", fullPage: true });
  });

  test("should edit a maintenance window", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // Create one first
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Maintenance" }).click();
    await page.waitForURL(/\/maintenance-windows/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-mw-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    const originalTitle = `E2E Edit Test ${Date.now()}`;
    await page.getByTestId("mw-title-input").fill(originalTitle);
    const now = new Date();
    await page.getByTestId("mw-start-input").fill(new Date(now.getTime() + 3600000).toISOString().slice(0, 16));
    await page.getByTestId("mw-end-input").fill(new Date(now.getTime() + 7200000).toISOString().slice(0, 16));
    await page.getByTestId("mw-submit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[0-9a-f]{8}-/, { timeout: 10000 });

    // Navigate to edit
    await page.locator('a[href*="/edit"]').click();
    await page.waitForURL(/\/edit$/);
    await page.waitForLoadState("networkidle");

    // Update title
    const updatedTitle = `${originalTitle} Edited`;
    await page.getByTestId("mw-title-input").fill(updatedTitle);
    await page.getByTestId("mw-submit-button").click();

    // Verify updated title on detail page
    await page.waitForURL(/\/maintenance-windows\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });
    await expect(page.getByRole("heading", { name: updatedTitle })).toBeVisible();
    await page.screenshot({ path: "test-results/screenshots/mw-edited.png", fullPage: true });
  });

  test("should delete a maintenance window", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // Create one first
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Maintenance" }).click();
    await page.waitForURL(/\/maintenance-windows/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-mw-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("mw-title-input").fill(`E2E Delete Test ${Date.now()}`);
    const now = new Date();
    await page.getByTestId("mw-start-input").fill(new Date(now.getTime() + 3600000).toISOString().slice(0, 16));
    await page.getByTestId("mw-end-input").fill(new Date(now.getTime() + 7200000).toISOString().slice(0, 16));
    await page.getByTestId("mw-submit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[0-9a-f]{8}-/, { timeout: 10000 });

    // Delete
    const deleteButton = page.locator('button:has([class*="lucide-trash"])');
    await deleteButton.click();
    await page.getByRole("button", { name: "Delete" }).click();

    // Should navigate back to list
    await page.waitForURL(/\/maintenance-windows$/, { timeout: 10000 });
    expect(page.url()).toMatch(/\/maintenance-windows$/);
    await page.screenshot({ path: "test-results/screenshots/mw-after-delete.png", fullPage: true });
  });
});
```

---

## 11. Integration Tests

**New file:** `back/test/integration/maintenance_windows_test.go`

Tests use the existing `TestServer` helper with in-memory SQLite and the generated API client.

```go
func TestMaintenanceWindows(t *testing.T) {
    t.Parallel()
    r := require.New(t)
    ts := NewTestServer(t)
    ctx := t.Context()

    // Login
    loginResp, err := ts.Client.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
    r.NoError(err)
    _ = loginResp
}
```

### Test cases

1. **CRUD lifecycle**: Create a maintenance window → Get by UID → List (verify it appears) → Update title → Get (verify updated) → Delete → List (verify gone)

2. **List with status filter**:
   - Create a window with `startAt` in the past and `endAt` in the future → status=active should include it
   - Create a window with `startAt` in the future → status=upcoming should include it
   - Create a window with `endAt` in the past → status=past should include it
   - status=all returns all three

3. **Check association**: Create a maintenance window and a check → Set window checks with the check UID → List window checks → Verify the check appears → Set with empty arrays → Verify cleared

4. **Group association**: Create a check group and a check in that group → Create a maintenance window associated with the group → Verify `IsCheckInActiveMaintenance` returns true for the check

5. **Incident suppression during active window**: Create a check → Create a currently-active maintenance window for that check → Process a failing check result → Verify no incident is created

6. **No suppression outside window**: Create a check → Create a past maintenance window for that check → Process a failing check result → Verify an incident IS created

7. **Recurring window detection**: Create a weekly recurring window with `startAt` = 8 days ago (2h duration) → Verify `IsCheckInActiveMaintenance` detects the recurring occurrence that overlaps with "now" (if applicable) or returns false (if not in the 2h window right now). Test with a controlled time or by setting startAt such that the current time falls within a recurrence.

---

## 12. Implementation Order

1. **Migrations** — new migration files (PostgreSQL + SQLite, up + down)
2. **Models** — `maintenance_window.go` with recurrence helper
3. **db.Service interface** — add method signatures
4. **DB implementations** — PostgreSQL + SQLite (both files)
5. **Error codes** — add to `base.go`
6. **Handler package** — `maintenancewindows/service.go` + `maintenancewindows/handler.go`
7. **Route registration** — update `server.go`
8. **Incident integration** — modify `ProcessCheckResult` in `incidents/service.go`
9. **Verify backend** — `make build` + `make test` + curl tests
10. **dash0: API hooks** — types + hooks in `hooks.ts`
11. **dash0: Sidebar** — add "Maintenance" nav entry
12. **dash0: Route files** — all 6 route files + form component
13. **dash0: Verify frontend** — `make dev-test`, navigate to maintenance pages
14. **status0: Maintenance badge** — backend flag + frontend rendering
15. **E2E tests** — `maintenance-windows.spec.ts`
16. **Integration tests** — `maintenance_windows_test.go`
17. **Verify end-to-end** — full flow testing

---

## 13. Key Files

| File | Change |
|------|--------|
| `back/internal/db/postgres/migrations/..._maintenance_windows.up.sql` | **New** — migration |
| `back/internal/db/sqlite/migrations/..._maintenance_windows.up.sql` | **New** — migration |
| `back/internal/db/models/maintenance_window.go` | **New** — models + recurrence helper |
| `back/internal/db/service.go` | Add MaintenanceWindow interface methods |
| `back/internal/db/postgres/postgres.go` | Implement MaintenanceWindow operations |
| `back/internal/db/sqlite/sqlite.go` | Implement MaintenanceWindow operations |
| `back/internal/handlers/base/base.go` | Add `MAINTENANCE_WINDOW_NOT_FOUND` |
| `back/internal/handlers/maintenancewindows/service.go` | **New** — business logic |
| `back/internal/handlers/maintenancewindows/handler.go` | **New** — HTTP handlers |
| `back/internal/handlers/incidents/service.go` | Add maintenance check in `ProcessCheckResult` |
| `back/internal/app/server.go` | Register maintenance window routes |
| `apps/dash0/src/api/hooks.ts` | Add MaintenanceWindow types + hooks |
| `apps/dash0/src/components/layout/AppSidebar.tsx` | Add "Maintenance" sidebar entry |
| `apps/dash0/src/components/shared/maintenance-window-form.tsx` | **New** — form component |
| `apps/dash0/src/routes/orgs/$org/maintenance-windows.tsx` | **New** — layout |
| `apps/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx` | **New** — list page |
| `apps/dash0/src/routes/orgs/$org/maintenance-windows.new.tsx` | **New** — create page |
| `apps/dash0/src/routes/orgs/$org/maintenance-windows.$uid.tsx` | **New** — detail layout |
| `apps/dash0/src/routes/orgs/$org/maintenance-windows.$uid.index.tsx` | **New** — detail page |
| `apps/dash0/src/routes/orgs/$org/maintenance-windows.$uid.edit.tsx` | **New** — edit page |
| `apps/dash0/e2e/maintenance-windows.spec.ts` | **New** — E2E tests |
| `back/test/integration/maintenance_windows_test.go` | **New** — integration tests |

---

## 14. Verification

### Backend
```bash
make build
SP_DB_RESET=true make dev-backend

TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create a maintenance window
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"DB Backup","startAt":"2026-03-22T02:00:00Z","endAt":"2026-03-22T04:00:00Z","recurrence":"weekly"}' \
  'http://localhost:4000/api/v1/orgs/default/maintenance-windows'

# List maintenance windows
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/maintenance-windows'

# List with status filter
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/maintenance-windows?status=active'

# Set associated checks
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"checkUid":["CHECK_UID"]}' \
  'http://localhost:4000/api/v1/orgs/default/maintenance-windows/MW_UID/checks'
```

### Frontend
```bash
make dev-test
# Navigate to /orgs/default/maintenance-windows
# Create a maintenance window with check selection
# Verify list shows status badges
# Edit and delete maintenance windows
```

### Tests
```bash
make test         # backend (includes integration tests)
make lint         # linting
cd apps/dash0 && bun run e2e  # E2E tests
```
