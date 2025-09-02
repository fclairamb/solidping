# Bulk Checks Tab on Test Page

## Overview

Add a "Bulk Checks" tab to the existing Test Tools page. This tab provides a form to create and delete large numbers of checks using the `/api/v1/test/checks/bulk` endpoint, enabling performance testing with hundreds or thousands of active checks.

## Motivation

The bulk test checks API (POST/DELETE `/api/v1/test/checks/bulk`) was added for performance testing, but currently requires manual curl commands. A dashboard tab makes it accessible to anyone running in test mode.

## Design

### Route Structure

Convert the test page from a single page to a tabbed layout, following the same pattern as the Account page (`account.tsx` + `account.profile.tsx` + `account.tokens.tsx`):

- `test.tsx` - Layout with TabNav and `<Outlet />`
- `test.index.tsx` - Redirect to `/orgs/$org/test/templates`
- `test.templates.tsx` - Existing test page content (template cards + custom form)
- `test.bulk.tsx` - New bulk checks tab

### Tabs

| Tab | Path | Content |
|-----|------|---------|
| Templates | `/orgs/$org/test/templates` | Existing template cards + custom form (moved from `test.tsx`) |
| Bulk Checks | `/orgs/$org/test/bulk` | New bulk create/delete form |

### Bulk Checks Tab UI

A single Card with a form containing:

**Create section:**
- **Count** (number input, 1-10000, default 100) - Number of checks to create
- **Slug template** (text input, default `http-{nb}`) - Must contain `{nb}`
- **URL template** (text input, default `http://localhost:4000/api/v1/fake?nb={nb}`) - URL with `{nb}` placeholder
- **Period** (text input, default `10s`) - Check interval as Go duration string
- **Create** button - Calls `POST /api/v1/test/checks/bulk` with query params

**Delete section:**
- **Count** and **Slug template** fields (same values as create section, shared state)
- **Delete** button (destructive variant) - Calls `DELETE /api/v1/test/checks/bulk`

**Results display:**
- After create: show `Created X checks (Y failed)` with first/last slug
- After delete: show `Deleted X checks`
- Errors displayed in a collapsible list if any

### API Integration

The bulk API uses query parameters (not JSON body), so the hooks will build URLs with query strings:

```typescript
// In hooks.ts
interface BulkCreateResponse {
  created: number;
  failed: number;
  errors?: string[];
  firstSlug?: string;
  lastSlug?: string;
}

interface BulkDeleteResponse {
  deleted: number;
}

export function useBulkCreateChecks() {
  return useMutation({
    mutationFn: ({ org, type, slug, url, period, count }: BulkCreateParams) => {
      const params = new URLSearchParams({ type, slug, count: String(count), org });
      if (url) params.set("url", url);
      if (period) params.set("period", period);
      return apiFetch<BulkCreateResponse>(`/api/v1/test/checks/bulk?${params}`, {
        method: "POST",
      });
    },
  });
}

export function useBulkDeleteChecks() {
  return useMutation({
    mutationFn: ({ org, slug, count }: BulkDeleteParams) => {
      const params = new URLSearchParams({ slug, count: String(count), org });
      return apiFetch<BulkDeleteResponse>(`/api/v1/test/checks/bulk?${params}`, {
        method: "DELETE",
      });
    },
  });
}
```

## Files to Create/Modify

| File | Action |
|------|--------|
| `apps/dash0/src/routes/orgs/$org/test.tsx` | Modify: convert to tab layout (TabNav + Outlet) |
| `apps/dash0/src/routes/orgs/$org/test.index.tsx` | Create: redirect to `/test/templates` |
| `apps/dash0/src/routes/orgs/$org/test.templates.tsx` | Create: move existing content here |
| `apps/dash0/src/routes/orgs/$org/test.bulk.tsx` | Create: bulk checks form |
| `apps/dash0/src/api/hooks.ts` | Modify: add `useBulkCreateChecks` and `useBulkDeleteChecks` hooks |
