# WebSocket Monitoring

## Overview

Add WebSocket monitoring as a new check type. Verifies WebSocket endpoints accept connections and optionally respond to messages. Supported by Uptime Kuma and Gatus.

## Goals

1. Verify WebSocket endpoints are reachable and accept upgrade
2. Optionally send a message and validate the response
3. Support both `ws://` and `wss://` (TLS)
4. Measure connection and response time

---

## Check Type

Type: `websocket`

### Settings

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | yes | — | WebSocket URL (`ws://` or `wss://`) |
| `headers` | map[string]string | no | — | Custom HTTP headers for upgrade request (e.g., `Authorization`) |
| `send` | string | no | — | Message to send as a text frame after connection |
| `expect` | string | no | — | Regex pattern to match against the first received message |
| `tls_skip_verify` | bool | no | false | Skip TLS certificate verification |
| `timeout` | duration | no | 10s | Connection and read timeout (max 60s) |

No database migration needed — `config` is stored as JSONB in the `checks` table.

### Execution Modes

**Mode 1 — Handshake-only** (no `send` or `expect` set):
1. Perform WebSocket handshake (HTTP upgrade) to `url` with optional `headers`
2. If connection upgrade succeeds → StatusUp
3. Close connection cleanly with close frame

**Mode 2 — Send/Expect** (`send` and/or `expect` set):
1. Perform WebSocket handshake to `url`
2. If `send` is provided, send the message as a text frame
3. If `expect` is provided, read the next message and match against pattern
4. Close connection cleanly with close frame

### Success Criteria (StatusUp)
- WebSocket handshake succeeds (HTTP 101 Switching Protocols)
- If `expect` is set, the received message matches the regex pattern

### Failure Criteria (StatusDown)
- Connection refused or reset
- HTTP upgrade rejected (non-101 response)
- TLS handshake failure (when using `wss://`)
- `expect` pattern does not match received message
- No message received when `expect` is set (within timeout)

### Timeout (StatusTimeout)
- TCP connection or WebSocket handshake exceeds timeout
- Message read exceeds timeout

### Error (StatusError)
- DNS resolution failure
- Invalid configuration (e.g., malformed URL, invalid regex)
- Network unreachable

---

## Go Types

### Config (`back/internal/checkers/checkwebsocket/config.go`)

```go
type WebSocketConfig struct {
    URL           string            `json:"url"`
    Headers       map[string]string `json:"headers,omitempty"`
    Send          string            `json:"send,omitempty"`
    Expect        string            `json:"expect,omitempty"`
    TLSSkipVerify bool              `json:"tls_skip_verify,omitempty"`
    Timeout       time.Duration     `json:"timeout,omitempty"`
}
```

`FromMap` must handle type coercion for `timeout` (duration string) and `headers` (map[string]any → map[string]string). `GetConfig` omits zero-value/default fields.

### Validation

Add to `WebSocketChecker.Validate()`:
- `url` must not be empty
- `url` must start with `ws://` or `wss://`
- `url` must be a valid URL (parseable by `net/url`)
- `timeout` must be > 0 and <= 60s (if set)
- `expect`, if set, must compile as valid regex
- `send` without `expect` is allowed (fire-and-forget)
- `expect` without `send` is allowed (server may push a message on connect)

---

## Backend Implementation

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkers/checkwebsocket/config.go` | `WebSocketConfig` struct with `FromMap`, `GetConfig` |
| `back/internal/checkers/checkwebsocket/checker.go` | `WebSocketChecker` with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checkwebsocket/checker_test.go` | Table-driven tests |
| `back/internal/checkers/checkwebsocket/errors.go` | Custom error types (`ErrInvalidConfigType`) |
| `back/internal/checkers/checkwebsocket/samples.go` | Sample configurations |

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeWebSocket CheckType = "websocket"` |
| `back/internal/checkers/registry/registry.go` | Add `checkwebsocket` cases to `GetChecker()` and `ParseConfig()` |
| `back/go.mod` | Add `nhooyr.io/websocket` dependency |

### WebSocket library

Use `nhooyr.io/websocket` (v2) — it is context-aware, has a simpler API than gorilla/websocket, and supports both `ws://` and `wss://`. It handles the HTTP upgrade internally.

### Execution flow (`checker.go`)

```go
func (c *WebSocketChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
    cfg := config.(*WebSocketConfig)
    params := newExecParams(cfg) // apply defaults
    ctx, cancel := context.WithTimeout(ctx, params.timeout)
    defer cancel()

    start := time.Now()
    metrics := map[string]any{}
    output := map[string]any{}

    // Configure TLS
    opts := &websocket.DialOptions{
        HTTPHeader: http.Header{},
    }
    if cfg.TLSSkipVerify {
        opts.HTTPClient = &http.Client{
            Transport: &http.Transport{
                TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
            },
        }
    }
    for k, v := range cfg.Headers {
        opts.HTTPHeader.Set(k, v)
    }

    // Connect
    handshakeStart := time.Now()
    conn, resp, err := websocket.Dial(ctx, cfg.URL, opts)
    handshakeDuration := time.Since(handshakeStart)
    metrics["handshake_time_ms"] = float64(handshakeDuration.Milliseconds())

    if err != nil {
        // handle connection error → StatusDown or StatusTimeout
    }
    defer conn.CloseNow()

    output["status_code"] = resp.StatusCode
    output["url"] = cfg.URL

    // Send message if configured
    if cfg.Send != "" {
        err = conn.Write(ctx, websocket.MessageText, []byte(cfg.Send))
        if err != nil { /* handle */ }
    }

    // Read and match response if configured
    if cfg.Expect != "" {
        _, msg, err := conn.Read(ctx)
        responseTime := time.Since(handshakeStart) - handshakeDuration
        metrics["response_time_ms"] = float64(responseTime.Milliseconds())
        if err != nil { /* handle timeout/error */ }

        output["received"] = string(msg[:min(len(msg), 4096)])

        re := regexp.MustCompile(cfg.Expect) // already validated
        if !re.Match(msg) {
            // StatusDown — pattern mismatch
        }
    }

    // Clean close
    conn.Close(websocket.StatusNormalClosure, "check complete")

    return &checkerdef.Result{
        Status:   checkerdef.StatusUp,
        Duration: time.Since(start),
        Metrics:  metrics,
        Output:   output,
    }, nil
}
```

### Metrics

Returned in `Result.Metrics`:
- `handshake_time_ms` (float64) — WebSocket handshake (HTTP upgrade) duration
- `response_time_ms` (float64) — Time from send to receive (only if `expect` is used)
- `total_time_ms` (float64) — Total check duration

### Output

Returned in `Result.Output`:
- `url` (string) — The URL connected to
- `status_code` (int) — HTTP response status code from upgrade (101)
- `received` (string) — First 4KB of received message (if `expect` is used)
- `error` (string) — Error message if check failed

---

## Dashboard UI

### Check Form (`check-form.tsx`)

#### Type Registration

Add to the `checkTypes` array:
```typescript
{ value: "websocket", label: "WebSocket", description: "Check WebSocket endpoint" },
```

#### Form Fields

Add `case "websocket"` to `renderConfigFields()`:

**Always shown:**
- **URL** — `Input` (required, text, placeholder: `wss://echo.websocket.org`, `data-testid="check-url-input"`)

**Optional section** (collapsible `Card` labeled "Message & Validation"):
- **Send Message** — `Textarea` (placeholder: `{"type":"ping"}`, hint: "Text message to send after connection", `data-testid="check-ws-send-input"`)
- **Expected Response** — `Input` (text, placeholder: `"type":\s*"pong"`, hint: "Regex pattern to match against the response", `data-testid="check-ws-expect-input"`)

**Advanced section** (collapsible `Card` labeled "Advanced"):
- **Custom Headers** — Key-value pairs editor or `Textarea` (JSON format, placeholder: `{"Authorization": "Bearer token"}`, `data-testid="check-ws-headers-input"`)
- **Skip TLS Verify** — `Checkbox` (`data-testid="check-ws-tls-skip-verify"`)

#### Config Submission

Add to the `handleSubmit` switch:
```typescript
case "websocket":
  if (!url) {
    setError("URL is required");
    return;
  }
  if (!url.startsWith("ws://") && !url.startsWith("wss://")) {
    setError("URL must start with ws:// or wss://");
    return;
  }
  config.url = url;
  if (wsSend) config.send = wsSend;
  if (wsExpect) config.expect = wsExpect;
  if (wsHeaders) config.headers = JSON.parse(wsHeaders);
  if (tlsSkipVerify) config.tls_skip_verify = true;
  break;
```

#### Default Period

In `defaultPeriod()`, WebSocket uses `"00:01:00"` (1 minute) — falls into the default case.

### Check List Target Display

Update the target display logic in `CheckRow` to handle WebSocket:

```typescript
check.type === "websocket"
  ? check.config?.url
  : // ... existing logic
```

### Translations

Add to `apps/dash0/src/locales/en/checks.json`:

```json
{
  "websocketUrl": "WebSocket URL",
  "websocketSend": "Send Message",
  "websocketExpect": "Expected Response",
  "websocketHeaders": "Custom Headers",
  "websocketTlsSkipVerify": "Skip TLS Verification",
  "websocketMessageSection": "Message & Validation",
  "websocketAdvancedSection": "Advanced"
}
```

Add French translations to `fr/checks.json`.

---

## Tests

### Config Tests (`config_test.go`)

Table-driven tests:

| Test case | Description |
|-----------|-------------|
| Minimal valid config | `url` only → defaults applied |
| All fields populated | Every field set → all parsed correctly |
| Timeout parsing | `"5s"` string → `5 * time.Second` |
| Headers parsing | map[string]any → map[string]string |
| Missing URL | → `ConfigError{Parameter: "url"}` |
| Invalid URL scheme | `http://...` → validation error |
| Invalid URL format | `wss://` (empty host) → validation error |
| Timeout out of range | `"120s"` → validation error |
| Invalid expect regex | `"[invalid"` → validation error |
| GetConfig round-trip | Populate all fields → GetConfig → FromMap → same values |

### Checker Tests (`checker_test.go`)

Integration tests using a test WebSocket server (use `httptest.NewServer` with `nhooyr.io/websocket` to create an echo server in the test):

| Test case | Description |
|-----------|-------------|
| Handshake only | Connect to echo server → StatusUp |
| Send and expect | Send `"ping"`, expect `"ping"` (echo) → StatusUp |
| Expect mismatch | Send `"hello"`, expect `"world"` → StatusDown |
| Custom headers | Headers passed through to server |
| TLS skip verify | Connect to self-signed `wss://` server → StatusUp |
| Connection refused | Invalid port → StatusDown |
| Timeout | Unresponsive server → StatusTimeout |
| Invalid URL | Malformed URL → StatusError |
| Expect without send | Server pushes a welcome message → StatusUp |

No testcontainers needed — an in-process `httptest.NewServer` with WebSocket upgrade handler is sufficient.

---

## Sample Configurations (`samples.go`)

```go
func (c *WebSocketChecker) GetSampleConfigs(_ *ListSampleOptions) []CheckSpec {
    return []CheckSpec{
        {
            Name:   "WebSocket Echo",
            Slug:   "ws-echo",
            Period: time.Minute,
            Config: (&WebSocketConfig{
                URL: "wss://echo.websocket.org",
            }).GetConfig(),
        },
        {
            Name:   "WebSocket Ping/Pong",
            Slug:   "ws-ping-pong",
            Period: time.Minute,
            Config: (&WebSocketConfig{
                URL:    "wss://api.example.com/ws",
                Send:   `{"type":"ping"}`,
                Expect: `"type":\s*"pong"`,
            }).GetConfig(),
        },
        {
            Name:   "WebSocket with Auth",
            Slug:   "ws-auth",
            Period: time.Minute,
            Config: (&WebSocketConfig{
                URL:     "wss://api.example.com/ws",
                Headers: map[string]string{"Authorization": "Bearer YOUR_TOKEN"},
                Send:    `{"action":"health"}`,
                Expect:  `"status":\s*"ok"`,
            }).GetConfig(),
        },
    }
}
```

---

## JSON Examples

### Handshake-only check

```json
{
  "type": "websocket",
  "config": {
    "url": "wss://echo.websocket.org"
  }
}
```

### Send/Expect with custom headers

```json
{
  "type": "websocket",
  "config": {
    "url": "wss://api.example.com/ws",
    "headers": {
      "Authorization": "Bearer eyJhbGciOi..."
    },
    "send": "{\"type\":\"ping\"}",
    "expect": "\"type\":\\s*\"pong\"",
    "timeout": "15s"
  }
}
```

### Self-signed TLS

```json
{
  "type": "websocket",
  "config": {
    "url": "wss://internal.corp:8443/ws",
    "tls_skip_verify": true,
    "send": "health",
    "expect": "ok"
  }
}
```

---

## E2E Tests (`apps/dash0/e2e/checks.spec.ts`)

### Test: Create a WebSocket check

```typescript
test("should create a new WebSocket check", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  // Navigate to new check form
  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Select WebSocket type
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /WebSocket/i }).click();

  // Fill in the form
  const checkName = `E2E WebSocket ${Date.now()}`;
  await page.getByTestId("check-name-input").fill(checkName);
  await page.getByTestId("check-url-input").fill("wss://echo.websocket.org");

  // Submit the form
  await page.getByTestId("check-submit-button").click();

  // Wait for navigation to check detail page
  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");

  // Verify check name is displayed
  await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

  // Take screenshot
  await page.screenshot({
    path: "test-results/screenshots/checks-websocket-created.png",
    fullPage: true,
  });
});
```

### Test: Create a WebSocket check with send/expect

```typescript
test("should create a WebSocket check with send/expect", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  // Navigate to new check form
  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Select WebSocket type
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /WebSocket/i }).click();

  // Fill in the form
  const checkName = `E2E WS Send ${Date.now()}`;
  await page.getByTestId("check-name-input").fill(checkName);
  await page.getByTestId("check-url-input").fill("wss://echo.websocket.org");
  await page.getByTestId("check-ws-send-input").fill('{"type":"ping"}');
  await page.getByTestId("check-ws-expect-input").fill('"type":\\s*"pong"');

  // Submit the form
  await page.getByTestId("check-submit-button").click();

  // Wait for navigation to check detail page
  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");

  // Verify check name is displayed
  await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

  // Take screenshot
  await page.screenshot({
    path: "test-results/screenshots/checks-websocket-send-expect.png",
    fullPage: true,
  });
});
```

### Test: Validation error for missing URL

```typescript
test("should show validation error for WebSocket when URL is empty", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Select WebSocket type
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /WebSocket/i }).click();

  // Fill name only, leave URL empty
  await page.getByTestId("check-name-input").fill("WS Missing URL");
  await page.getByTestId("check-submit-button").click();

  await page.waitForTimeout(500);
  expect(page.url()).toContain("/checks/new");
  await expect(page.getByText(/URL is required/i)).toBeVisible();
});
```

### Test: Validation error for invalid URL scheme

```typescript
test("should show validation error for WebSocket with invalid URL scheme", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Select WebSocket type
  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /WebSocket/i }).click();

  // Fill with HTTP URL instead of WS
  await page.getByTestId("check-name-input").fill("WS Bad Scheme");
  await page.getByTestId("check-url-input").fill("https://example.com");
  await page.getByTestId("check-submit-button").click();

  await page.waitForTimeout(500);
  expect(page.url()).toContain("/checks/new");
  await expect(page.getByText(/URL must start with ws:\/\/ or wss:\/\//i)).toBeVisible();
});
```

---

## Security Considerations

- Custom headers may contain auth tokens — stored in JSONB `config` column, already access-controlled per organization
- `tls_skip_verify` should be used sparingly — log a warning in output when enabled
- Received message is truncated to 4KB to prevent excessive storage
- Timeout applies to the entire check including message exchange to prevent hanging connections

---

## Key Files

| File | Action |
|------|--------|
| `back/internal/checkers/checkwebsocket/config.go` | New |
| `back/internal/checkers/checkwebsocket/checker.go` | New |
| `back/internal/checkers/checkwebsocket/errors.go` | New |
| `back/internal/checkers/checkwebsocket/samples.go` | New |
| `back/internal/checkers/checkwebsocket/checker_test.go` | New |
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeWebSocket` constant |
| `back/internal/checkers/registry/registry.go` | Register WebSocket in both switches |
| `apps/dash0/src/components/shared/check-form.tsx` | Add WebSocket type, form fields, config submission |
| `apps/dash0/e2e/checks.spec.ts` | Add 4 WebSocket E2E tests |

## Verification

1. **Backend tests**: `make test` — all pass including new `checkwebsocket` tests
2. **Linting**: `make lint` — no errors
3. **Manual test**: `make dev-test`, create WebSocket check targeting `wss://echo.websocket.org` via the UI
4. **E2E tests**: `cd apps/dash0 && bun run test:e2e`
5. **API test**:
   ```bash
   TOKEN=$(cat /tmp/token.txt)
   curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"WS Echo","slug":"ws-echo","type":"websocket","config":{"url":"wss://echo.websocket.org"}}' \
     'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
   ```

## Competitor Reference

- **Uptime Kuma**: WebSocket connection monitoring
- **Gatus**: WebSocket endpoint type

**Status**: Draft | **Created**: 2026-03-22
