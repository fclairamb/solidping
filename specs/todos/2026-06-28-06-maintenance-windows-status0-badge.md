# Maintenance Windows — status0 "Scheduled Maintenance" Badge

## Context

When a check is inside an active maintenance window, the **public** status page (status0)
should signal "Scheduled Maintenance" instead of showing a raw up/down state, so visitors
understand the dip is planned. The maintenance-windows backend and incident suppression
already ship; this spec adds the small public-payload flag plus the status0 rendering.

Independent of the dash0 management UI
([`2026-06-28-05-maintenance-windows-dash0-ui.md`](2026-06-28-05-maintenance-windows-dash0-ui.md)),
but lower value without it (you need the UI, or the API/MCP, to create windows in the first
place). Ship after, or in parallel with, that spec.

> Origin: frontend section 9 of `specs/done/2026/03/2026-03-22-maintenance-windows.md`,
> rewritten against the real code (that spec's paths/shapes are stale).

---

## Current state (verified in source)

- Public status payload builds `ResourceCheckInfo` per resource in
  `server/internal/handlers/statuspages/service.go` — struct at lines 122–127, populated in
  `getCheckInfo` (lines 1404–1438). Fields today: `name`, `type`, `status`. **No maintenance flag.**
- db.Service already exposes
  `ListMaintenanceWindowsForCheck(ctx, checkUID string) ([]*models.MaintenanceWindow, error)`
  (`server/internal/db/service.go:465`), and `models.IsActiveAt(window, t)` evaluates active state.
- status0 renders each resource in `ResourceCard` inside
  `web/status0/src/components/shared/status-page-view.tsx` — status dot + status `Badge` via the
  `statusStyle(status)` helper (`@/lib/status-style`), labels via `t()` (react-i18next). Badge
  variants include `warning` (amber).

---

## Tasks

### 1. Backend — expose `inMaintenance` on the public resource payload

`server/internal/handlers/statuspages/service.go`:

a. Add the field to `ResourceCheckInfo` (after `Status`):
```go
InMaintenance bool `json:"inMaintenance"`
```

b. Populate it in `getCheckInfo` before returning. The check is already loaded; query its windows
   and test "active now":
```go
inMaintenance := false
if windows, errMW := s.db.ListMaintenanceWindowsForCheck(ctx, checkUID); errMW == nil {
    now := time.Now()
    for _, w := range windows {
        if models.IsActiveAt(w, now) {
            inMaintenance = true
            break
        }
    }
}
// …
return &ResourceCheckInfo{
    Name:          check.Name,
    Type:          check.Type,
    Status:        statusStr,
    InMaintenance: inMaintenance,
}, nil
```
Do **not** fail the page on a maintenance-lookup error — log/ignore and default `false` (the public
page must stay up). `ListMaintenanceWindowsForCheck` runs once per resource; resource counts per page
are small, so the extra query is acceptable. If a page can carry many resources, consider a
per-request memo or reuse of the incidents 60s cache helper — optional, not required.

### 2. status0 — render the badge

`web/status0/src/components/shared/status-page-view.tsx`, in `ResourceCard`:

- Read `const inMaintenance = resource.check?.inMaintenance ?? false;`
- When `inMaintenance` is true, render an amber **"Scheduled Maintenance"** `Badge variant="warning"`
  in place of the normal status `Badge` (keep the status dot, optionally muted). When false, keep the
  current rendering unchanged.
- Use a translated label: `t("scheduledMaintenance")` (see §3). Add `data-testid="resource-maintenance-badge"`
  for the E2E assertion.

Update the status0 TypeScript type for the public resource's `check` to include
`inMaintenance?: boolean` (wherever status0 types the status-page API response).

### 3. i18n

status0 uses react-i18next (the component already calls `t()`). Add a `scheduledMaintenance`
label ("Scheduled Maintenance") to the status0 locale files (`web/status0/src/locales/…` — match
the existing namespace that holds the status labels used in this component). Translate for every
language status0 ships.

---

## Tests

### Backend
Extend the statuspages service tests (same package/pattern as existing public-payload tests):
- Resource whose check is in an **active** window → response `check.inMaintenance == true`.
- Resource whose check is only in a **past/upcoming** window → `false`.
- Resource with no windows → `false`.
Follow `server/CLAUDE.md`: `testify/require`, `t.Parallel()`, table-driven.

### status0 E2E (if status0 has a Playwright suite; otherwise manual)
With a check placed in a currently-active maintenance window and shown on a public status page,
load the public page and assert the `resource-maintenance-badge` ("Scheduled Maintenance") is visible.

---

## Key files

| File | Change |
|---|---|
| `server/internal/handlers/statuspages/service.go` | `+ InMaintenance` field + populate in `getCheckInfo` |
| `server/internal/handlers/statuspages/*_test.go` | `+` cases for the maintenance flag |
| `web/status0/src/components/shared/status-page-view.tsx` | `+` maintenance badge in `ResourceCard` |
| status0 public-resource TS type | `+ inMaintenance?: boolean` |
| `web/status0/src/locales/**` | `+ scheduledMaintenance` label (all languages) |

---

## Verification

```bash
make dev-test
# 1. Create a check, and a maintenance window active "now" covering it
#    (via API/MCP, or the dash0 UI once spec 05 lands).
# 2. Add the check as a resource on a public status page.
# 3. Open the public page (status0): the resource shows an amber
#    "Scheduled Maintenance" badge instead of its raw up/down status.
make test   # backend (statuspages service tests)
```

---

## Implementation Plan

### Backend payload field (`server/internal/handlers/statuspages/service.go`)
- Add `InMaintenance bool \`json:"inMaintenance"\`` to `ResourceCheckInfo` (after `Status`, lines 122–127).
- Compute it in `getCheckInfo` (the check is already loaded): call
  `s.db.ListMaintenanceWindowsForCheck(ctx, checkUID)` and, if no error, loop the windows testing
  `models.IsActiveAt(w, time.Now())`. Default `false` on any lookup error (never fail the public
  page). Populate the new field in the returned struct. `s.db` (`db.Service`) already exposes the
  method; `models`, `time`, `context` are already imported.

### status0 type (`web/status0/src/api/hooks.ts`)
- Add `inMaintenance?: boolean;` to `ResourceCheckInfo`.

### status0 rendering (`web/status0/src/components/shared/status-page-view.tsx`, `ResourceCard`)
- Read `const inMaintenance = resource.check?.inMaintenance ?? false;`.
- When true: in the right-hand badge slot, render an amber
  `<Badge variant="warning" data-testid="resource-maintenance-badge">{t("scheduledMaintenance")}</Badge>`
  in place of the normal status `Badge` (status dot kept). When false: current rendering unchanged.

### i18n (`web/status0/src/locales/{en,fr,de,es}/status.json`)
- Add top-level key `scheduledMaintenance` ("Scheduled Maintenance" / translated) to the `status`
  namespace in all four locales.

### Tests
- Backend (`server/internal/handlers/statuspages/service_test.go`, real in-memory sqlite via
  `setupStatusPagesTest`): one table-driven test exercising `getCheckInfo` for a check with
  (a) an active window → `InMaintenance == true`, (b) a past window only → `false`,
  (c) no windows → `false`. Uses `CreateMaintenanceWindow` + `SetMaintenanceWindowChecks`.
  `testify/require`, `t.Parallel()`.
- status0 E2E: status0 has no Playwright suite (only `tsc -b && vite build` + `eslint .`), so the
  badge is covered by build/lint + the backend flag test; manual verification per the spec's
  Verification block.
