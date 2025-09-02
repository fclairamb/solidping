# HTTP Request/Response Logging for CLI Client

## Purpose
Enable debugging and development of the frontend by providing visibility into the exact HTTP requests and responses made by the CLI client.

## Goal
Make it easy for frontend developers to see exactly what HTTP calls are being made, so they can replicate the same calls in the browser/frontend application.

## Requirements

### Functional
- Log raw HTTP requests (method, URL, headers, body)
- Log raw HTTP responses (status code, headers, body)
- Display output in a browser-compatible format (e.g., format that could be copy-pasted into browser dev tools or used with fetch/axios)
- Only log when explicitly enabled (opt-in, not opt-out)
- Pretty-print JSON request/response bodies for readability
- Include request counter for easy correlation between request and response

### Security
- **DO NOT redact sensitive data** - The purpose of this logging is to help developers replicate exact requests
- Users must explicitly enable this feature (opt-in) and understand it logs sensitive data
- Output goes to stderr only (not to files or slog)

### Technical
- **Environment variable**: Enable with `SP_LOG_HTTP_CALLS=1` or `SP_LOG_HTTP_CALLS=true`
- **Implementation location**: `back/pkg/client/client.go` in the `New()` function
- **Approach**: Use a custom `http.RoundTripper` wrapper that logs requests/responses, then set it as the transport in `ClientOption`
- **NOT using slog**: Must NOT use the structured logging system (slog) - we need raw, unformatted HTTP output
- **Output destination**: Plain text to stderr (`os.Stderr`), showing exact request/response as they would appear in network tools
- **JSON formatting**: Use `json.Indent()` to pretty-print JSON bodies (detect by Content-Type header)

## Implementation Details

### Architecture
1. Create a new type `loggingRoundTripper` that wraps `http.RoundTripper`
2. In the `RoundTrip()` method:
   - Log the request (method, URL, headers, body)
   - Call the wrapped `RoundTripper.RoundTrip()`
   - Log the response (status, headers, body)
   - Return the response
3. Check `SP_LOG_HTTP_CALLS` environment variable in `client.New()`
4. If enabled, add the logging transport via `WithHTTPClient` option

### Code Location
- File: `back/pkg/client/client.go`
- Modify: `New()` function to check env var and configure logging transport
- Add: `loggingRoundTripper` type and implementation

### Request Counter
- Use a simple counter (can be global or in the roundtripper struct)
- Increment for each request
- Include in log output as "Request N" and "Response N"

### JSON Detection and Formatting
- Check if `Content-Type` header contains `application/json`
- If JSON: Read body, pretty-print with 2-space indentation, restore body for actual request
- If not JSON: Print raw body (or indicate binary content)

### Error Handling
- If body reading fails, log error but don't fail the request
- If JSON parsing fails, log raw body instead
- Ensure request body is always restored for the actual HTTP call

## Example Output
```
=== Request 1 ===
POST /api/v1/orgs/default/auth/login HTTP/1.1
Host: localhost:4000
Content-Type: application/json

{
  "email": "admin@solidping.com",
  "password": "solidpass"
}
=== / Request 1 ===

=== Response 1 ===
HTTP/1.1 200 OK
Content-Type: application/json

{
  "accessToken": "eyJhbGc...",
  "refreshToken": "eyJhbGc...",
  "user": {
    "uid": "...",
    "email": "admin@solidping.com"
  }
}
=== / Response 1 ===

=== Request 2 ===
GET /api/v1/orgs/default HTTP/1.1
Host: localhost:4000
Authorization: Bearer eyJhbGc...
=== / Request 2 ===

=== Response 2 ===
HTTP/1.1 200 OK
Content-Type: application/json

{
  "uid": "default",
  "name": "Default Organization",
  "slug": "default"
}
=== / Response 2 ===
```

## Acceptance Criteria
- [x] When `SP_LOG_HTTP_CALLS=1`, all HTTP requests and responses are logged to stderr
- [x] When `SP_LOG_HTTP_CALLS` is not set, no HTTP logging occurs
- [x] Request and response pairs are numbered sequentially
- [x] JSON bodies are pretty-printed with 2-space indentation
- [x] Non-JSON bodies are printed as-is (or marked as binary)
- [x] Headers are printed exactly as sent/received (including Authorization headers with full tokens)
- [x] The actual HTTP requests are not affected by logging (bodies are properly restored)
- [x] Log output can be easily copy-pasted and used to replicate requests with curl or browser fetch

## Out of Scope
- No filtering or redacting of sensitive data (passwords, tokens, etc.) - this is a debugging tool
- No file output - stderr only
- No integration with slog or structured logging
- No request/response size limits - log everything
- No colorization or fancy formatting - plain text only
- No configuration beyond the single environment variable

## Testing Approach
Manual testing:
1. Set `SP_CLIENT_LOG_HTTP_CALLS=1`
2. Run `sp auth login` and verify request/response are logged
3. Run `sp checks list` and verify request/response are logged
4. Verify JSON is pretty-printed
5. Verify request counter increments correctly
6. Unset the env var and verify no logging occurs
