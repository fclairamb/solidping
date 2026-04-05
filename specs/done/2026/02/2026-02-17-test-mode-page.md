# Test Mode Page

## Overview

Add a test-only page accessible from the sidebar (with a bug icon) that appears only when `runMode === "test"`. This page lets developers quickly create HTTP checks against the built-in fake API with pre-filled configurations, making it easy to populate the system with realistic monitoring scenarios.

## Motivation

1. In test mode, developers need to quickly seed checks with known failure patterns to exercise the dashboard, incidents, and alerting flows.
2. The fake API at `/api/v1/fake` supports many parameters, but remembering them is tedious. Pre-filled templates remove that friction.
3. Currently, sample checks are only created at startup. This page lets developers add checks on demand at any time.

## Current State

- The fake API (`GET /api/v1/fake`) supports `period`, `statusUp`, `statusDown`, `delay`, `format`, `requiredAuth`, `requiredHeader`, `slowResponse` parameters.
- `useVersion()` hook already fetches `runMode` from `/api/mgmt/version`.
- `useCreateCheck()` mutation creates checks via `POST /api/v1/orgs/{org}/checks`.
- The sidebar navigation is defined in `AppSidebar.tsx` as a static `navItems` array.
- The check form component (`check-form.tsx`) handles all check types with type-specific config fields.

---

## Frontend Changes

### 1. Conditional sidebar item

**File**: `apps/dash0/src/components/layout/AppSidebar.tsx`

Add `Bug` to the lucide-react imports.

Add a separate `testNavItems` array:
```typescript
const testNavItems = [
  {
    title: "Test Tools",
    path: "/orgs/$org/test" as const,
    icon: Bug,
  },
];
```

In the `<SidebarContent>`, after the main `<SidebarGroup>`, render a second group conditionally:

```tsx
{isTestMode && (
  <SidebarGroup>
    <SidebarGroupContent>
      <SidebarMenu>
        {testNavItems.map((item) => { /* same rendering as navItems */ })}
      </SidebarMenu>
    </SidebarGroupContent>
  </SidebarGroup>
)}
```

Use the `useVersion()` hook inside `AppSidebar` to determine `isTestMode`:
```typescript
const { data: versionData } = useVersion();
const isTestMode = versionData?.runMode === "test";
```

### 2. Test tools page

**New file**: `apps/dash0/src/routes/orgs/$org/test.tsx`

A page with:
- Title: "Test Tools"
- Subtitle: "Create pre-configured checks against the fake API"
- A grid of check template cards, each with a "Create" button

**Template cards layout**: `grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4`

Each card shows:
- Name and description of the check scenario
- Key parameters at a glance (period, fake API cycle, expected behavior)
- A "Create" button that calls `useCreateCheck()` with pre-filled data
- After creation, a toast notification with a link to the new check

**Pre-filled check templates** (derived from `docs/testing/http-test-checks.md`):

| Template | Name | Period | URL suffix | Description |
|----------|------|--------|------------|-------------|
| Stable | Fake API (Stable) | `00:00:10` | `?period=86400` | Always up (24h cycle) |
| Flaky | Fake API (Flaky) | `00:00:15` | `?period=120` | Up 60s, down 60s |
| Unstable | Fake API (Unstable) | `00:00:15` | `?period=40` | Up 20s, down 20s |
| Slow | Fake API (Slow) | `00:00:20` | `?period=86400&delay=2000` | Always up, 2s response delay |
| 503 errors | Fake API (503) | `00:00:15` | `?period=60&statusDown=503` | Returns 503 when down |

Each template constructs a `CreateCheckRequest`:
```typescript
{
  name: "Fake API (Stable)",
  type: "http",
  config: {
    url: `${window.location.origin}/api/v1/fake?period=86400`,
    method: "GET",
    expected_status: 200,
  },
  period: "00:00:10",
  enabled: true,
}
```

The URL is built using `window.location.origin` so it works regardless of where the app is hosted.

**Additional section: Custom check form**

Below the template cards, add an expandable section ("Create custom fake API check") with a simple form:
- **Period** (seconds): How often to check (input, default: 15)
- **Fake API cycle** (seconds): The `period` param for the fake API (input, default: 120)
- **Down status code**: HTTP status when down (input, default: 500)
- **Response delay** (ms): Artificial latency (input, default: 0)
- "Create" button

This builds a URL like:
```
{origin}/api/v1/fake?period={cycle}&statusDown={code}&delay={delay}
```

### 3. Route file generation

After creating the route file, run `bun run dev` or the TanStack Router code generator to update `routeTree.gen.ts`.

---

## Backend Changes

No backend changes required. The page uses the existing:
- `GET /api/mgmt/version` for `runMode` detection
- `POST /api/v1/orgs/{org}/checks` for check creation
- `GET /api/v1/fake` as the target for created checks

---

## Key Files

| File | Change |
|------|--------|
| `apps/dash0/src/components/layout/AppSidebar.tsx` | Add conditional "Test Tools" nav item with Bug icon |
| `apps/dash0/src/routes/orgs/$org/test.tsx` | **New** — Test tools page with template cards and custom form |

---

## Verification

### Manual
1. Start with `SP_RUNMODE=test make dev-backend` and `cd apps/dash0 && bun run dev`
2. Log in with test credentials (`test@test.com` / `test`)
3. Verify the Bug icon "Test Tools" item appears in the sidebar
4. Click each template card's "Create" button and verify checks appear in the Checks list
5. Verify the custom form creates checks with the specified parameters
6. Start with `SP_RUNMODE=` (default mode) and verify the "Test Tools" item does NOT appear in the sidebar

### Lint
```bash
cd apps/dash0 && bun run lint
```

---

**Status**: Draft | **Created**: 2026-02-17
