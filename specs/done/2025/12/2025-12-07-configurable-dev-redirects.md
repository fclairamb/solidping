# Configurable Development Redirects

**Type**: chore
**Branch**: `chore/configurable-dev-redirects`

## Summary

Replace the hard-coded proxy logic that redirects `localhost:4000` to `localhost:5173` with a configurable redirect system using the `SP_REDIRECTS` environment variable.

## Current Behavior

The current implementation in `back/internal/app/server.go` (lines 156-173):
- Checks if `req.Host == "localhost:4000"`
- If true, proxies all requests to `localhost:5173`
- This is hard-coded and inflexible

## Proposed Behavior

Allow configuring redirects via the `SP_REDIRECTS` environment variable with the format:

```
SP_REDIRECTS=/path:target_host,/path2:target_host2
```

### Format Specification

- **Separator between rules**: `,` (comma)
- **Separator between path and target**: `:` (colon) after the path
- **Path**: Must start with `/`, represents the URL path prefix to match
- **Target**: Full host (with optional port) to proxy to, can include a target path

### Examples

```bash
# Single redirect: proxy /dashboard/* to localhost:5173/dashboard/*
SP_REDIRECTS=/dashboard:localhost:5173/dashboard

# Multiple redirects
SP_REDIRECTS=/dashboard:localhost:5173/dashboard,/status:localhost:5174/status

# Redirect everything (catch-all)
SP_REDIRECTS=/:localhost:5173
```

### Matching Rules

1. Redirects are matched by longest path prefix first
2. The matched path prefix is replaced with the target path
3. Query strings and remaining path are preserved

### Example Request Flow

With `SP_REDIRECTS=/dashboard:localhost:5173/app`:
- Request: `GET /dashboard/settings?tab=profile`
- Proxied to: `http://localhost:5173/app/settings?tab=profile`

## Acceptance Criteria

- [ ] `SP_REDIRECTS` environment variable is parsed correctly
- [ ] Multiple redirect rules can be specified
- [ ] Path matching works with prefix matching
- [ ] Longer path prefixes take precedence over shorter ones
- [ ] Query strings are preserved when proxying
- [ ] Invalid redirect format logs a warning and is skipped
- [ ] Empty `SP_REDIRECTS` disables all redirects (serves static files)
- [ ] Requests not matching any redirect rule serve static files

## Technical Considerations

- Configuration should be loaded at startup in `config.go`
- The redirect map should be sorted by path length (longest first) for matching
- Reverse proxy should properly handle WebSocket upgrades for Vite HMR
- Logging should indicate when a redirect is being used

## Implementation Plan

### Files to Modify

1. **`back/internal/config/config.go`**
   - Add `Redirects` field to `ServerConfig` struct
   - Add `RedirectRule` struct with `PathPrefix` and `Target` fields
   - Add parsing logic for `SP_REDIRECTS` environment variable
   - Sort redirect rules by path length (longest first)

2. **`back/internal/app/server.go`**
   - Update `Config` struct to include redirect rules
   - Modify `serveAppRoot()` to check redirect rules instead of hard-coded host check
   - Update `serveAppRedirect()` to use the matched redirect rule's target
   - Handle path rewriting (replace matched prefix with target path)

### New Types

```go
// RedirectRule represents a path-based redirect configuration
type RedirectRule struct {
    PathPrefix string // e.g., "/dashboard"
    TargetHost string // e.g., "localhost:5173"
    TargetPath string // e.g., "/dashboard" or "/app"
}
```

### Parsing Logic

1. Split `SP_REDIRECTS` by `,` to get individual rules
2. For each rule, parse format: `/path:host:port/targetpath` or `/path:host:port`
3. Extract path prefix (everything before first `:`)
4. Extract target (everything after first `:`)
5. Parse target into host and optional path
6. Sort rules by `PathPrefix` length descending

### Request Matching Flow

1. For each incoming request, iterate through sorted redirect rules
2. Check if `req.URL.Path` starts with `rule.PathPrefix`
3. If match found:
   - Build new path: `rule.TargetPath + strings.TrimPrefix(req.URL.Path, rule.PathPrefix)`
   - Proxy to `rule.TargetHost` with the new path
4. If no match, serve static files

### Testing Strategy

- Unit tests for redirect rule parsing
- Unit tests for path matching and rewriting
- Integration test with actual proxy (optional)

## Implementation Notes

(To be filled during implementation)
