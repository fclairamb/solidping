# Frontend Error Handling Conventions

## Rules

These are mandatory conventions for handling HTTP errors in the frontend.

### Authentication Errors

| Status | Action | Redirect |
|--------|--------|----------|
| **401** | Clear token, redirect to login | `/orgs/{org}/login?returnTo={currentPath}` |
| **403** | Show "Permission Denied" message | **Never redirect to login** |

### Client Errors

| Status | Action |
|--------|--------|
| **400/422** | Show inline validation errors in forms |
| **404** | Show "Not Found" page with navigation options |
| **429** | Show rate limit message with countdown/retry |

### Server Errors

| Status | Action | Retry |
|--------|--------|-------|
| **500** | Show "Something went wrong" with retry button | Manual only |
| **502/503/504** | Auto-retry with exponential backoff (max 3) | Automatic |

### Network Errors

| Error | Action |
|-------|--------|
| Connection lost | Show banner, auto-retry with backoff |

## Key Principles

1. **Never redirect 403 to login** - causes infinite loops for authenticated but unauthorized users
2. **Preserve navigation on 401** - use `returnTo` query param to restore user's location after login
3. **Auto-retry transient errors** - 502/503/504 are often temporary (deployments, scaling)
4. **User-friendly messages** - never show raw error responses to users
5. **Validate returnTo** - must start with `/orgs/` to prevent open redirect attacks

## React Query Configuration

```tsx
retry: (failureCount, error) => {
  // Only retry 5xx errors
  if (error instanceof ApiError && error.status >= 500) {
    return failureCount < 3;
  }
  return false;
},
retryDelay: (attemptIndex) => Math.min(1000 * 2 ** attemptIndex, 10000),
```

## See Also

- [Full Specification](/specs/2026-02-05-frontend-error-handling.md)
