# Refresh Checks List After Adding a Check

## Overview

When a new check is created via the dash0 UI, the checks list should automatically refresh to show the newly added check. Currently, the user must manually reload the page to see new checks appear.

## Current Behavior

1. User creates a new check via the check creation form
2. Check is saved successfully on the backend
3. The checks list still shows stale data until a manual page refresh or the next auto-refresh cycle (30s)

## Expected Behavior

1. User creates a new check via the check creation form
2. Check is saved successfully on the backend
3. The checks list immediately refreshes and displays the new check

## Implementation

After a successful check creation (mutation succeeds), invalidate the TanStack Query cache for the checks query so it refetches automatically.

Use `queryClient.invalidateQueries()` targeting the checks query key after the create mutation's `onSuccess` callback.

## Files Likely Involved

- Check creation form/mutation (wherever the POST to create a check is triggered)
- Checks list query (the query key to invalidate)

## Implementation Plan

1. **Root cause**: `useCreateCheck` invalidates `["checks", org]` but the checks index uses `useInfiniteChecks` with key `["checks", "infinite", org, options]`. TanStack Query prefix matching doesn't match across the "infinite" segment.
2. **Fix**: In `useCreateCheck`, `useUpdateCheck`, and `useDeleteCheck` in `web/dash0/src/api/hooks.ts`, also invalidate `["checks", "infinite", org]` alongside the existing invalidation.
