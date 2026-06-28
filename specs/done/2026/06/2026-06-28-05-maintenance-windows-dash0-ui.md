# Maintenance Windows — dash0 Management UI

## Context

The maintenance-windows **backend is fully shipped** (DB schema, models, db.Service,
handlers, REST API, incident suppression, MCP tools). What is missing is the **dash0
dashboard UI** — today the only way to create/list/edit/delete maintenance windows is
the raw REST API or MCP tools. This spec adds the management UI.

The companion spec
[`2026-06-28-06-maintenance-windows-status0-badge.md`](2026-06-28-06-maintenance-windows-status0-badge.md)
adds the public "Scheduled Maintenance" badge on status0. This spec is independent of
that one and can ship first.

> Origin: this is the frontend half of
> `specs/done/2026/03/2026-03-22-maintenance-windows.md`. That spec's frontend sections
> (8–10) were never implemented and contain **stale paths** (`back/`, `apps/dash0/`) and a
> **stale API contract**. Use THIS spec — the contract below was read from the shipped code.

---

## Current state (do NOT re-implement)

Already in `main`, verified against source:

| Surface | Location |
|---|---|
| Model + recurrence helper `IsActiveAt` | `server/internal/db/models/maintenance_window.go` |
| db.Service methods (incl. `ListMaintenanceWindowsForCheck`) | `server/internal/db/service.go` |
| Handlers | `server/internal/handlers/maintenancewindows/{handler,service}.go` |
| Routes registered | `server/internal/app/server.go:958` |
| Incident suppression (60s cache) | `server/internal/handlers/incidents/service.go` |
| User-facing docs | `web/docs/docs/features/maintenance-windows.md` |

**dash0 has zero maintenance-window code** — no hooks, no routes, no nav entry, no form.

---

## Real API contract (read from shipped handler/service)

Base: `/api/v1/orgs/:org/maintenance-windows` — authenticated, org-scoped.

| Method | Path | Body | Success |
|---|---|---|---|
| GET | `…?status=&limit=` | — | `200 { "data": [Window] }` |
| POST | `…` | `CreateRequest` | `201 Window` (bare object) |
| GET | `…/:uid` | — | `200 Window` |
| PATCH | `…/:uid` | `UpdateRequest` (all optional) | `200 Window` |
| DELETE | `…/:uid` | — | `204` |
| GET | `…/:uid/checks` | — | `200 { "data": [CheckAssoc] }` |
| PUT | `…/:uid/checks` | `SetChecksRequest` | `204` |

**List query params:** `status` ∈ `active|upcoming|past|""(all)`; `limit` default 50, max 100. **No cursor pagination.**

**`Window` response shape** (exactly these fields — note what's absent):
```json
{
  "uid": "…",
  "title": "Weekly DB Backup",
  "description": "…",            // omitted when null
  "startAt": "2026-03-22T02:00:00Z",
  "endAt":   "2026-03-22T04:00:00Z",
  "recurrence": "weekly",        // none | daily | weekly | monthly
  "recurrenceEnd": null,         // omitted when null
  "createdBy": "user-uid",       // omitted when null
  "createdAt": "…",
  "updatedAt": "…"
}
```
> ⚠ There is **NO** `status`, `checkCount`, or `checkGroupCount` field. The UI must compute
> status **client-side** (helper below) and derive check counts from the `/checks` endpoint.

**`CreateRequest`:** `{ title, description?, startAt, endAt, recurrence, recurrenceEnd? }` —
**does NOT accept checks inline.** Associating checks is a **separate** `PUT …/checks` call.

**`SetChecksRequest`:** `{ "checkUids": ["…"], "checkGroupUids": ["…"] }` — **plural, camelCase.**

**`CheckAssoc` response:** `{ uid, checkUid?, checkGroupUid? }` — UIDs only, **no names**. Resolve
names client-side from the org's checks / check-groups queries.

**Validation errors** come back as field-level validation errors (`title`, `endAt`, `recurrence`)
in the standard `{ title, code, detail }` + fields shape.

---

## Conventions to follow (verified in repo)

- **Design reference is mandatory** (`CLAUDE.md`): build from primitives shown in
  `web/dash0/src/routes/orgs/$org/design-reference.tsx`. Available: `Button`, `Table*`
  (`@/components/ui/table`), `Badge` (variants `default|secondary|destructive|outline|success|warning`,
  `@/components/ui/badge`), `Input`, `Textarea`, `Select*`, `Card*`, `AlertDialog*`,
  `PageHeader` (`@/components/shared/page-header`), `CheckPicker` (`@/components/shared/check-picker`,
  **single-value only**). **No date-picker primitive exists** → use native `<input type="datetime-local">`.
- **Hooks:** `web/dash0/src/api/hooks.ts`, `apiFetch<T>` from `@/api/client`, `org` is the first
  hook arg, query keys `["resource", org]` / `["resource", org, uid]`, list unwrapped via
  `response.data ?? []`, mutations `invalidateQueries` list + detail on success. Mirror the
  `useStatusPages` / `useStatusPage` / `useCreateStatusPage…` block.
- **Routes:** file-based under `web/dash0/src/routes/orgs/$org/`. Mirror `status-pages.*`
  (layout `<Outlet/>` + `.index` + `.new` + `.$<uid>` detail layout + `.$<uid>.index` + `.$<uid>.edit`).
  Params via `Route.useParams()`; navigate with `useNavigate()`.
- **Form:** mirror `web/dash0/src/components/shared/status-page-form.tsx` — controlled
  `useState` (not react-hook-form), props `{ mode: "create"|"edit", initialData?, isPending, onSubmit, onCancel }`.
- **i18n (react-i18next):** add a `maintenanceWindows` namespace JSON to **each** of
  `web/dash0/src/locales/{en,fr,de,es}/` and a `maintenanceWindows` key to each `nav.json`.
  Reference with `useTranslation("maintenanceWindows")`. **No hardcoded English strings.**
- **Mobile-first & error handling** (`CLAUDE.md`): responsive layout, large touch targets;
  401→login `?returnTo`, 403→"Permission Denied" (no redirect). Delete is always the red
  `Trash2` (`variant="destructive"`).
- **Edit on a dedicated route**, never a modal.

---

## Tasks

### 1. API types + hooks — `web/dash0/src/api/hooks.ts`

Add interfaces:
```ts
export interface MaintenanceWindow {
  uid: string;
  title: string;
  description?: string;
  startAt: string;
  endAt: string;
  recurrence: "none" | "daily" | "weekly" | "monthly";
  recurrenceEnd?: string;
  createdBy?: string;
  createdAt: string;
  updatedAt: string;
}
export interface MaintenanceWindowCheck { uid: string; checkUid?: string; checkGroupUid?: string; }
export interface CreateMaintenanceWindowRequest {
  title: string; description?: string; startAt: string; endAt: string;
  recurrence: string; recurrenceEnd?: string | null;
}
export type UpdateMaintenanceWindowRequest = Partial<CreateMaintenanceWindowRequest>;
export interface SetMaintenanceWindowChecksRequest { checkUids: string[]; checkGroupUids: string[]; }
```

Add hooks (mirror status-pages):
- `useMaintenanceWindows(org, params?: { status?: string; limit?: number })` → GET list, unwrap `data`.
- `useMaintenanceWindow(org, uid)` → GET one.
- `useMaintenanceWindowChecks(org, uid)` → GET `…/:uid/checks`, unwrap `data`.
- `useCreateMaintenanceWindow(org)` → POST, returns the created `MaintenanceWindow`.
- `useUpdateMaintenanceWindow(org, uid)` → PATCH.
- `useDeleteMaintenanceWindow(org)` → DELETE (arg: uid).
- `useSetMaintenanceWindowChecks(org, uid)` → PUT `…/:uid/checks`.

Mutation `onSuccess` invalidates `["maintenanceWindows", org]` (+ `["maintenanceWindow", org, uid]`
and `["maintenanceWindowChecks", org, uid]` where relevant).

Reuse existing `useChecks(org)` / `useCheckGroups(org)` to resolve names and to populate the pickers
(confirm exact hook names in `hooks.ts`; adjust if they differ).

### 2. Client-side status helper — `web/dash0/src/lib/maintenance-window-status.ts` (new)

Port `models.IsActiveAt` to TS (the response has no server-computed status):
```ts
import type { MaintenanceWindow } from "@/api/hooks";
export type MaintenanceStatus = "active" | "upcoming" | "past";

function step(d: Date, recurrence: string): Date {
  const n = new Date(d);
  if (recurrence === "daily") n.setDate(n.getDate() + 1);
  else if (recurrence === "weekly") n.setDate(n.getDate() + 7);
  else if (recurrence === "monthly") n.setMonth(n.getMonth() + 1);
  return n;
}
export function isActiveAt(w: MaintenanceWindow, t: Date): boolean {
  const start = new Date(w.startAt), end = new Date(w.endAt);
  if (w.recurrence === "none") return t >= start && t < end;
  if (w.recurrenceEnd && t > new Date(w.recurrenceEnd)) return false;
  if (t < start) return false;
  const durationMs = end.getTime() - start.getTime();
  let cur = new Date(start);
  for (;;) { const next = step(cur, w.recurrence); if (next > t) break; cur = next; }
  return t >= cur && t < new Date(cur.getTime() + durationMs);
}
export function computeMaintenanceStatus(w: MaintenanceWindow, now = new Date()): MaintenanceStatus {
  if (isActiveAt(w, now)) return "active";
  if (w.recurrence === "none") return now < new Date(w.startAt) ? "upcoming" : "past";
  if (w.recurrenceEnd && now > new Date(w.recurrenceEnd)) return "past";
  return "upcoming";
}
```
Status→Badge variant: `active`→`success`, `upcoming`→`secondary`, `past`→`outline`.

### 3. Sidebar entry — `web/dash0/src/components/layout/AppSidebar.tsx`

Import `Wrench` (or `CalendarClock`) from `lucide-react`; add to `navItems` after `incidents`:
```ts
{ titleKey: "maintenanceWindows", path: "/orgs/$org/maintenance-windows" as const, icon: Wrench },
```
Not admin-gated. Add `"maintenanceWindows"` to each `nav.json`.

### 4. Multi-select picker — `web/dash0/src/components/shared/check-multi-picker.tsx` (new)

`CheckPicker` is single-value; the form needs to pick **multiple** checks **and** multiple check
groups. Add a reusable multi-select (checks + groups) returning `{ checkUids, checkGroupUids }`,
rendering selected items as removable `Badge` chips. **Register it in `design-reference.tsx`** (the
catalog must stay canonical — `CLAUDE.md`). If a simpler approach fits the existing catalog (e.g. two
`Select` lists + chip rows), prefer reuse over a new abstraction, but still showcase it in the reference.

### 5. Shared form — `web/dash0/src/components/shared/maintenance-window-form.tsx` (new)

Controlled-state form (mirror `status-page-form.tsx`). Props: `{ mode, initialData?, initialChecks?, isPending, onSubmit, onCancel }`.

Fields + `data-testid`:
| Field | Control | testid |
|---|---|---|
| Title (required) | `Input` | `mw-title-input` |
| Description (optional) | `Textarea` | `mw-description-input` |
| Start | `<input type="datetime-local">` | `mw-start-input` |
| End | `<input type="datetime-local">` | `mw-end-input` |
| Recurrence | `Select` (None/Daily/Weekly/Monthly) | `mw-recurrence-select` |
| Recurrence end (only when recurrence≠none) | `<input type="datetime-local">` | `mw-recurrence-end-input` |
| Checks (multi) | `CheckMultiPicker` | `mw-checks-select` |
| Check groups (multi) | `CheckMultiPicker` | `mw-groups-select` |
| Submit | `Button` | `mw-submit-button` |

Client validation before submit: title non-empty; end after start; recurrence-end (if shown)
after start. Surface field errors inline. `onSubmit` emits
`{ window: CreateMaintenanceWindowRequest, checkUids: string[], checkGroupUids: string[] }`.
Datetime conversion: form uses `datetime-local` (local, no zone) → convert to RFC3339 UTC for the API;
convert API UTC → local for prefill.

### 6. Routes — `web/dash0/src/routes/orgs/$org/` (param `$maintenanceWindowUid`)

| File | Purpose |
|---|---|
| `maintenance-windows.tsx` | Layout `<Outlet/>` |
| `maintenance-windows.index.tsx` | List page |
| `maintenance-windows.new.tsx` | Create |
| `maintenance-windows.$maintenanceWindowUid.tsx` | Detail layout `<Outlet/>` |
| `maintenance-windows.$maintenanceWindowUid.index.tsx` | Detail view |
| `maintenance-windows.$maintenanceWindowUid.edit.tsx` | Edit |

**Create flow is two-step** (API can't take checks inline):
```ts
const w = await createMaintenanceWindow.mutateAsync(window);
if (checkUids.length || checkGroupUids.length)
  await setChecks(org, w.uid).mutateAsync({ checkUids, checkGroupUids });
toast.success(t("toast.created"));
navigate({ to: "/orgs/$org/maintenance-windows/$maintenanceWindowUid",
           params: { org, maintenanceWindowUid: w.uid } });
```
**Edit flow:** load window (GET) + its checks (GET `/checks`) for prefill; on save PATCH window
**and** PUT checks; navigate back to detail.

### 7. List page (`maintenance-windows.index.tsx`)

- `PageHeader` title `t("title")` + "New" button → `/maintenance-windows/new`, `data-testid="mw-new-button"`.
- `Table` columns: Title, Schedule (formatted `startAt`–`endAt`), Recurrence, Status `Badge`
  (from `computeMaintenanceStatus`), Checks count (from `useMaintenanceWindowChecks`).
- Row click → detail. Row actions: ghost `Pencil` (edit) + ghost red `Trash2` (delete, `AlertDialog`
  confirm). Per `CLAUDE.md` prefer two ghost icon buttons over a `MoreVertical` menu.
- Row testids: `mw-row-view-{uid}`, `mw-row-edit-{uid}`, `mw-row-delete-{uid}`.
- Empty state (Card): `t("empty")` ("No maintenance windows scheduled").
- Loading: existing skeleton/loader; error: existing `QueryErrorView`.

### 8. Detail page (`…$maintenanceWindowUid.index.tsx`)

- Header: title + status `Badge`. Description if present.
- Schedule `Card`: start, end, recurrence, recurrence end.
- Affected checks `Card`: resolve `checkUid`/`checkGroupUid` → check / group names with links to those
  resources. Empty hint if none.
- Actions: Edit (`Pencil` → `.edit`), Delete (red `Trash2` + `AlertDialog`; on confirm navigate to list).

### 9. i18n keys

Create `web/dash0/src/locales/{en,fr,de,es}/maintenanceWindows.json` (translate all four — en authoritative):
`title`, `new`, `empty`, `form.{title,description,start,end,recurrence,recurrenceEnd,checks,groups,submit}`,
`recurrence.{none,daily,weekly,monthly}`, `status.{active,upcoming,past}`,
`detail.{schedule,affectedChecks}`, `toast.{created,updated,deleted}`,
`delete.{confirmTitle,confirmBody}`. Add `"maintenanceWindows"` to each `nav.json`. Register the
namespace in `web/dash0/src/i18n.ts` if namespaces are listed explicitly there.

---

## E2E tests — `web/dash0/e2e/maintenance-windows.spec.ts` (new)

Use `./fixtures` `authenticatedPage` (test mode: org `test`, `test@test.com`/`test`). Cover, adapting
to the **real testids** and the **two-step create flow**:
1. List page renders (heading + `mw-new-button`), empty state visible on a clean DB.
2. New form: all `mw-*` inputs visible.
3. Create a one-time window (create a check first, select it via `mw-checks-select`), assert landing on
   detail with the title.
4. Create a weekly recurring window: choosing Weekly reveals `mw-recurrence-end-input`.
5. Edit a window's title → detail reflects the change.
6. Delete a window via row/detail red `Trash2` + confirm → returns to list.

Navigate via sidebar: `page.getByRole("link", { name: "Maintenance" }).click()` then
`waitForURL(/\/maintenance-windows/)`. Screenshot each into `test-results/screenshots/`.
Treat any flake as a bug to root-cause (capture logs/guards), never re-run blindly.

---

## Implementation order

1. Hooks + types (§1) and status helper (§2) — verify with existing API via curl.
2. i18n namespace + nav key (§3, §9).
3. Multi-picker (§4) + design-reference entry.
4. Form (§5).
5. Routes: list → new → detail → edit (§6–8), wiring the two-step create/edit.
6. `make dev-test`, click through create/list/edit/delete on mobile + desktop widths.
7. E2E spec (§E2E).
8. `make lint` + `make test-dash`. Keep dash0 lint to **no new errors** (base is red with pre-existing
   react-hooks errors — do not fix that debt here).

---

## Key files

| File | Change |
|---|---|
| `web/dash0/src/api/hooks.ts` | **+** MW types + hooks |
| `web/dash0/src/lib/maintenance-window-status.ts` | **New** — status helper |
| `web/dash0/src/components/layout/AppSidebar.tsx` | **+** nav entry |
| `web/dash0/src/components/shared/check-multi-picker.tsx` | **New** — multi-select |
| `web/dash0/src/components/shared/maintenance-window-form.tsx` | **New** — form |
| `web/dash0/src/routes/orgs/$org/maintenance-windows.tsx` | **New** — layout |
| `web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx` | **New** — list |
| `web/dash0/src/routes/orgs/$org/maintenance-windows.new.tsx` | **New** — create |
| `web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.tsx` | **New** — detail layout |
| `web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx` | **New** — detail |
| `web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.edit.tsx` | **New** — edit |
| `web/dash0/src/routes/orgs/$org/design-reference.tsx` | **+** showcase multi-picker |
| `web/dash0/src/locales/{en,fr,de,es}/maintenanceWindows.json` | **New** — i18n |
| `web/dash0/src/locales/{en,fr,de,es}/nav.json` | **+** `maintenanceWindows` |
| `web/dash0/e2e/maintenance-windows.spec.ts` | **New** — E2E |

---

## Verification

```bash
make dev-test   # backend + dash0, SP_RUNMODE=test, port 4000
# Sidebar → Maintenance → New: create one-time + recurring; select checks/groups
# List shows Active/Upcoming/Past badges; edit; delete (red trash + confirm)
# Resize to mobile width — fully usable
make test-dash  # Playwright E2E
make lint       # no NEW dash0 lint errors
```

Out of scope (separate follow-ups): status0 public badge (companion spec); backend Go
unit/integration tests for the `maintenancewindows` package.

---

## Implementation Plan

Backend contract re-confirmed against shipped code
(`server/internal/handlers/maintenancewindows/{handler,service}.go`,
`server/internal/db/models/maintenance_window.go`,
`server/internal/app/server.go:957`): base
`/api/v1/orgs/:org/maintenance-windows`, `:uid` in paths (UUID), `status` ∈
`active|upcoming|past|""`, `limit` ≤ 100, list wrapped in `{ data }`, POST returns
bare `201`, PATCH bare `200`, DELETE `204`, `GET/PUT …/:uid/checks`
(`{ checkUids, checkGroupUids }`). Response has **no** server status / counts.

### Files & order

1. **API layer** — `web/dash0/src/api/hooks.ts`: add `MaintenanceWindow`,
   `MaintenanceWindowCheck`, `CreateMaintenanceWindowRequest`,
   `UpdateMaintenanceWindowRequest`, `SetMaintenanceWindowChecksRequest` interfaces
   and hooks `useMaintenanceWindows(org, params?)`, `useMaintenanceWindow(org, uid)`,
   `useMaintenanceWindowChecks(org, uid)`, `useCreateMaintenanceWindow(org)`,
   `useUpdateMaintenanceWindow(org, uid)`, `useDeleteMaintenanceWindow(org)`,
   `useSetMaintenanceWindowChecks(org, uid)`. Query keys
   `["maintenanceWindows", org]` / `["maintenanceWindow", org, uid]` /
   `["maintenanceWindowChecks", org, uid]`. Reuse `useChecks` / `useCheckGroups` for
   name resolution. (Commit: feat api layer.)
2. **Status helper** — `web/dash0/src/lib/maintenance-window-status.ts` (new): port
   `models.IsActiveAt` → `isActiveAt` + `computeMaintenanceStatus` +
   `maintenanceStatusBadgeVariant` (active→success, upcoming→secondary, past→outline).
   (Commit: feat status helper.)
3. **i18n** — new `maintenanceWindows.json` in `locales/{en,fr,de,es}/`, register in
   `i18n.ts`, add `maintenanceWindows` key to each `nav.json`. Datetime helpers in
   the form file (local↔UTC RFC3339). (Commit: feat i18n + nav.)
4. **Multi-picker** — `web/dash0/src/components/shared/check-multi-picker.tsx` (new):
   Popover + search list (mirrors `check-picker.tsx`), selected items as removable
   `Badge` chips; props `{ org, kind: "checks"|"groups", value: string[], onChange }`.
   Showcase in `design-reference.tsx` (new "Check multi-picker" section + SECTIONS
   entry). (Commit: feat multi-picker + design-reference.)
5. **Form** — `web/dash0/src/components/shared/maintenance-window-form.tsx` (new):
   controlled state, all fields + `data-testid`s from §5, datetime-local ↔ UTC
   conversion, inline validation (title, end>start, recurrence-end>start), emits
   `{ window, checkUids, checkGroupUids }`. (Commit: feat form.)
6. **Routes** under `web/dash0/src/routes/orgs/$org/` (param `$maintenanceWindowUid`):
   `maintenance-windows.tsx` (Outlet), `.index.tsx` (list: PageHeader + New, Table
   Title/Schedule/Recurrence/Status/Checks, row Pencil+red Trash2 AlertDialog, empty
   state, QueryErrorView), `.new.tsx` (two-step create), `.$maintenanceWindowUid.tsx`
   (Outlet), `.$maintenanceWindowUid.index.tsx` (detail: header+status badge,
   schedule Card, affected-checks Card with links, Edit + red Trash2 delete),
   `.$maintenanceWindowUid.edit.tsx` (load window + checks, PATCH + PUT, back to
   detail). (Commits: feat list; feat new+edit+detail routes.)
7. **Sidebar** — `AppSidebar.tsx`: add `{ titleKey: "maintenanceWindows", path:
   "/orgs/$org/maintenance-windows", icon: Wrench }` after `statusUpdates` (not
   admin-gated). (Folded into i18n/nav commit or its own.)
8. **E2E** — `web/dash0/e2e/maintenance-windows.spec.ts` (new): list renders + empty,
   new form shows all `mw-*` inputs, create one-time (with a check) → detail, weekly
   reveals recurrence-end, edit title → detail, delete via red Trash2 → list.

### QA gates
`make build-dash0`, `make build-backend`, `make lint-back`, `make test`, dash0
`bun run lint` (no NEW errors), E2E authored (run if test-mode server available).
