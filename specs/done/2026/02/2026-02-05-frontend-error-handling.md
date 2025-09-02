# Frontend Error Handling Specification

## Overview

This specification defines how the frontend (dash0) should handle HTTP errors from the API to provide a consistent and user-friendly experience.

## Error Categories

### Authentication Errors

#### 401 Unauthorized
**Scenario**: Token expired, invalid, or missing.

**Behavior**:
1. Clear the stored authentication token
2. Redirect to the login page with the current path preserved:
   ```
   /orgs/{org}/login?returnTo=/orgs/{org}/checks/abc123
   ```
3. On successful login, redirect back to the `returnTo` URL
4. If `returnTo` is missing or invalid, redirect to the org dashboard

**Implementation notes**:
- The `returnTo` parameter should be URL-encoded
- Validate `returnTo` starts with `/orgs/` to prevent open redirect vulnerabilities
- Show a toast: "Your session has expired. Please log in again."

#### 403 Forbidden
**Scenario**: User is authenticated but lacks permission for the resource.

**Behavior**:
1. **Do NOT redirect to login** (this would cause infinite loops)
2. Show an inline error message: "You don't have permission to access this resource"
3. Provide a "Go Back" or "Return to Dashboard" button
4. Log the error for debugging

**Use cases**:
- User tries to access another organization's resources
- User lacks required role (e.g., admin-only features)

### Client Errors

#### 404 Not Found
**Scenario**: Requested resource doesn't exist.

**Behavior**:
1. Show a "Not Found" message specific to the resource type:
   - "Check not found" for `/checks/{uid}`
   - "Incident not found" for `/incidents/{uid}`
2. Provide navigation options: "Back to list" or "Go to Dashboard"
3. Don't show technical error details

#### 400 Bad Request / 422 Validation Error
**Scenario**: Invalid request data (form validation, malformed request).

**Behavior**:
1. Display field-specific validation errors inline in forms
2. Show a toast summarizing the issue
3. Do not navigate away from the current page
4. Allow the user to correct and retry

#### 429 Too Many Requests
**Scenario**: Rate limit exceeded.

**Behavior**:
1. Show a message: "Too many requests. Please wait..."
2. If `Retry-After` header is present, show a countdown
3. Automatically retry the request after the wait period (for background operations)
4. For user-initiated actions, show a "Retry" button that enables after the wait

### Server Errors

#### 500 Internal Server Error
**Scenario**: Unexpected server error.

**Behavior**:
1. Show a user-friendly error message: "Something went wrong on our end"
2. Display a "Retry" button for the failed operation
3. Optionally show error details in a collapsible section (for debugging)
4. Log the error with context (URL, timestamp, error code if available)

**Toast example**: "An unexpected error occurred. Please try again or contact support if the problem persists."

#### 502 Bad Gateway / 503 Service Unavailable / 504 Gateway Timeout
**Scenario**: Service temporarily unavailable (deployments, scaling, network issues).

**Behavior**:
1. Show a message: "Service temporarily unavailable"
2. **Auto-retry with exponential backoff**:
   - 1st retry: 1 second
   - 2nd retry: 2 seconds
   - 3rd retry: 4 seconds
   - Max 3 retries
3. Show a spinner during retry attempts
4. After max retries, show "Unable to connect. Please check your connection and try again."
5. Provide a manual "Retry" button

**Rationale**: These errors are often transient (during deployments, auto-scaling). Auto-retry improves UX without user intervention.

### Network Errors

#### No Connection / Network Failure
**Scenario**: Browser cannot reach the server.

**Behavior**:
1. Show a banner: "Connection lost. Retrying..."
2. Auto-retry with exponential backoff (same as 502/503/504)
3. When connection is restored, dismiss the banner automatically
4. Consider using `navigator.onLine` for proactive detection

## Implementation Architecture

### Global Error Boundary
Wrap the app in an error boundary to catch rendering errors:
```tsx
<ErrorBoundary fallback={<ErrorFallback />}>
  <App />
</ErrorBoundary>
```

### API Client Error Handling
The `apiFetch` function should:
1. Handle 401 redirects centrally
2. Throw typed `ApiError` for other errors
3. Components handle specific errors via try/catch or React Query's `onError`

### React Query Integration
Use React Query's error handling:
```tsx
const { error, refetch } = useQuery({
  queryKey: ['resource'],
  queryFn: fetchResource,
  retry: (failureCount, error) => {
    // Retry for 5xx errors, not for 4xx
    if (error instanceof ApiError && error.status >= 500) {
      return failureCount < 3;
    }
    return false;
  },
  retryDelay: (attemptIndex) => Math.min(1000 * 2 ** attemptIndex, 10000),
});
```

## Summary Table

| Status | Category | User Message | Action | Auto-Retry |
|--------|----------|--------------|--------|------------|
| 401 | Auth | "Session expired" | Redirect to login with `returnTo` | No |
| 403 | Auth | "Permission denied" | Show error, offer navigation | No |
| 404 | Client | "Resource not found" | Show error, offer navigation | No |
| 400/422 | Client | Field-specific errors | Inline form errors | No |
| 429 | Client | "Rate limited" | Show countdown, retry after | Yes (after delay) |
| 500 | Server | "Something went wrong" | Show retry button | No |
| 502/503/504 | Server | "Service unavailable" | Auto-retry with backoff | Yes (3 attempts) |
| Network | Infra | "Connection lost" | Auto-retry with backoff | Yes (3 attempts) |

## Security Considerations

1. **Open Redirect Prevention**: Validate `returnTo` parameter starts with `/orgs/` and doesn't contain external URLs
2. **Error Message Sanitization**: Never display raw server error messages to users (may leak sensitive info)
3. **Logging**: Log errors client-side for debugging but don't send sensitive data to external services

## Future Considerations

- **Offline Mode**: Cache critical data for offline access
- **Error Reporting**: Integration with error tracking service (e.g., Sentry)
- **Health Check Endpoint**: Proactive monitoring of API availability
