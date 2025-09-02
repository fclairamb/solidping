# Custom User-Agent Header

## Overview
Add a custom `User-Agent` header to all HTTP(S) health check requests made by SolidPing.

## Requirements
- Format: `SolidPing/$version` where `$version` is the application version
- Example: `User-Agent: SolidPing/1.2.3`
- Apply to all outgoing HTTP/HTTPS health check requests
- Use the version from the build-time version info

## Purpose
- Identify SolidPing as the source of health check requests
- Allow monitored services to recognize and potentially allowlist SolidPing checks
- Provide version information for debugging and compatibility tracking

## Implementation

### Files to Modify
1. **`back/internal/checkers/checkhttp/checker.go`** - Add User-Agent header to HTTP requests
2. **`back/internal/checkers/checkhttp/checker_test.go`** - Add tests for User-Agent header

### Technical Approach
1. Import the version package: `github.com/fclairamb/solidping/back/internal/version`
2. Set User-Agent header after creating the HTTP request (around line 130)
3. User-Agent should be set BEFORE custom headers so users can override if needed
4. Use `version.Version` to get the semantic version string

### Code Changes

**In `checker.go`:**
```go
// After creating the request (after line ~128)
req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, bytes.NewReader(bodyBytes))
if err != nil {
    return checkerdef.ResultError(err), nil
}

// Add default User-Agent header
req.Header.Set("User-Agent", fmt.Sprintf("SolidPing/%s", version.Version))

// Add custom headers (existing code at ~134-137)
for key, value := range cfg.Headers {
    req.Header.Set(key, value)
}
```

**Note**: Custom headers from config will override the default User-Agent if specified.

### Testing Strategy

**Add to `checker_test.go`:**

1. **Test default User-Agent is set**:
   - Create a test HTTP server that captures the User-Agent header
   - Execute a check with minimal config
   - Verify User-Agent matches `SolidPing/{version}`

2. **Test User-Agent can be overridden**:
   - Create a check with custom User-Agent in headers config
   - Verify custom User-Agent is used instead of default

3. **Integration with existing tests**:
   - Verify existing tests still pass
   - User-Agent should not break any existing functionality

**Example test structure**:
```go
func TestUserAgentHeader(t *testing.T) {
    // Test server that captures headers
    var capturedUA string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedUA = r.Header.Get("User-Agent")
        w.WriteHeader(http.StatusOK)
    }))
    defer ts.Close()

    checker := &HTTPChecker{}
    cfg := &HTTPConfig{URL: ts.URL}

    _, _ = checker.Execute(context.Background(), cfg)

    expected := fmt.Sprintf("SolidPing/%s", version.Version)
    assert.Equal(t, expected, capturedUA)
}
```

### Acceptance Criteria
- [ ] All HTTP/HTTPS health checks include `User-Agent: SolidPing/{version}` header
- [ ] Version number matches the build-time version from `version.Version`
- [ ] User-Agent can be overridden via custom headers in check config
- [ ] All existing tests pass
- [ ] New tests added for User-Agent behavior
- [ ] No breaking changes to existing HTTP check functionality

### Edge Cases
- If `version.Version` is empty or "0.0.0", still set the header (e.g., `SolidPing/0.0.0`)
- User-Agent should be set for all HTTP methods (GET, POST, PUT, DELETE, etc.)
- Custom User-Agent in config.Headers should take precedence
