# JavaScript Checker (Sandboxed Scripting)

## Overview

Add a JavaScript check type that executes user-written scripts in a sandboxed runtime. Scripts can orchestrate multiple sub-checks using existing SolidPing checkers, perform conditional logic, and compute composite health status. This enables complex monitoring scenarios that cannot be expressed with a single protocol checker: multi-step API workflows, cross-service health aggregation, business logic validation, and custom protocol handling.

The runtime is **goja** (`github.com/dop251/goja`), a pure-Go ES5.1+ engine with ES6 features (arrow functions, const/let, template literals, destructuring, for-of). Used in production by Grafana k6, Caddy, and CockroachDB. No CGO dependency. Built-in interrupt mechanism for timeout enforcement.

Comparable features: Checkly (API check scripts in Node.js), Grafana Synthetic Monitoring (k6 scripts via goja).

## Goals

1. Enable programmable, multi-step monitoring checks via JavaScript
2. Expose all existing checkers (HTTP, TCP, DNS, ICMP, SSL, Domain, SMTP) as callable functions within scripts
3. Provide a lightweight HTTP client for raw request/response handling
4. Run scripts in a secure sandbox with strict resource limits
5. Support environment variables for secret injection without hardcoding in scripts

---

## Check Type

Type: `js`

### Settings

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `script` | string | yes | — | JavaScript source code (max 64KB) |
| `timeout` | duration | no | 30s | Maximum execution time (max 60s) |
| `env` | map[string]string | no | — | Environment variables accessible via `env.KEY` in the script (max 50 entries) |

No database migration needed — `config` is stored as JSONB in the `checks` table.

Default check period: **5 minutes** (scripts are heavier than single protocol checks).

---

## Script API Reference

Scripts are synchronous function bodies. They run top-to-bottom and must `return` a result object. No async/await, no Promises, no setTimeout/setInterval.

### `solidping.check(type, config)` — Generic Checker

Executes any registered SolidPing checker by type. Returns a result object.

```javascript
var r = solidping.check("tcp", {host: "db.example.com", port: 5432});
// r = {status: "up", duration: 12.5, metrics: {...}, output: {...}}
```

**Blocked types**: `js` (prevents recursion), `heartbeat` (passive checker, cannot be executed).

### `solidping.http(config)`, `solidping.tcp(config)`, etc. — Typed Wrappers

Convenience shortcuts for `solidping.check(type, config)`:

```javascript
solidping.http({url: "https://example.com", expectedStatusCodes: ["200"]})
solidping.tcp({host: "db.example.com", port: 5432})
solidping.dns({domain: "example.com", recordType: "A"})
solidping.icmp({host: "8.8.8.8"})
solidping.ssl({host: "example.com"})
solidping.domain({domain: "example.com"})
solidping.smtp({host: "mail.example.com"})
```

Each returns:
```javascript
{
  status: "up" | "down" | "timeout" | "error",
  duration: 45.2,       // milliseconds
  metrics: { /* checker-specific */ },
  output: { /* checker-specific */ }
}
```

### `http.get(url, opts)`, `http.post(url, opts)`, etc. — Lightweight HTTP Client

For scripts that need raw HTTP responses (not the full HTTP checker with pattern matching):

```javascript
var resp = http.get("https://api.example.com/v1/status", {
    headers: {"Authorization": "Bearer " + env.API_KEY}
});
// resp = {statusCode: 200, body: "...", headers: {...}, duration: 45.2}

var resp2 = http.post("https://api.example.com/v1/data", {
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({query: "test"})
});
```

Methods: `http.get`, `http.post`, `http.put`, `http.patch`, `http.delete`, `http.head`.

Response body capped at 1MB. Counts toward the 20 sub-check limit.

### `console.log()`, `console.warn()`, `console.error()`, `console.info()`

Captured in `result.output.console` (max 16KB). Useful for debugging.

```javascript
console.log("Checking API...");
console.log("Response:", JSON.stringify(data));
```

### `env` — Environment Variables

Read-only object populated from the `env` config field:

```javascript
var apiKey = env.API_KEY;
var baseUrl = env.BASE_URL || "https://api.example.com";
```

### `sleep(ms)` — Controlled Delay

```javascript
sleep(1000); // wait 1 second between steps
```

Capped at remaining context timeout. Counts toward total execution time.

### `JSON.parse()` / `JSON.stringify()`

Available natively in the goja ES5.1 runtime.

---

## Return Value Contract

Scripts must `return` an object with a `status` field:

```javascript
return {
    status: "up",                              // required: "up", "down", "error", "timeout"
    metrics: {responseTime: 42, queueDepth: 5}, // optional: aggregatable numeric values
    output: {error: "details here"}             // optional: diagnostic data
};
```

### Mapping to `checkerdef.Result`

| Script return | Result |
|---|---|
| `{status: "up"}` | `StatusUp` |
| `{status: "down", output: {error: "..."}}` | `StatusDown` |
| `{status: "error"}` | `StatusError` |
| `{status: "timeout"}` | `StatusTimeout` |
| Script throws exception | `StatusError`, error message in output |
| Context timeout fires | `StatusTimeout` |
| No return / `undefined` | `StatusError`: "script did not return a result" |
| Missing `status` field | `StatusError`: "return value missing 'status' field" |
| Invalid status string | `StatusError`: "invalid status: must be up, down, error, or timeout" |

### Automatic fields added to result

- `result.Output["console"]` — captured console output
- `result.Output["subChecks"]` — number of sub-checks executed
- `result.Metrics["duration_ms"]` — total script execution time

---

## Go Types

### Config (`back/internal/checkers/checkjs/config.go`)

```go
const (
    maxScriptSize  = 64 * 1024 // 64KB
    maxEnvVars     = 50
    maxEnvKeyLen   = 128
    maxEnvValueLen = 4096
    defaultTimeout = 30 * time.Second
    maxTimeout     = 60 * time.Second
)

type JSConfig struct {
    Script  string            `json:"script"`
    Timeout time.Duration     `json:"timeout,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}
```

`FromMap` extracts `script` (string, required), `timeout` (duration string, optional), `env` (map[string]string, optional). `GetConfig` omits zero-value fields.

### Validation

- `script` must not be empty
- `script` must not exceed 64KB
- `script` must compile without syntax errors (use `goja.Compile` for early feedback)
- `timeout` must be > 0 and <= 60s (if set)
- `env` must have <= 50 entries
- `env` keys must be alphanumeric/underscore, max 128 chars
- `env` values max 4096 chars each

---

## Backend Implementation

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkers/checkjs/config.go` | `JSConfig` with `FromMap`, `GetConfig` |
| `back/internal/checkers/checkjs/checker.go` | `JSChecker` with `Type`, `Validate`, `Execute` |
| `back/internal/checkers/checkjs/runtime.go` | goja runtime setup, global injection, console capture |
| `back/internal/checkers/checkjs/bridge.go` | `CheckerBridge` wrapping existing checkers for JS access |
| `back/internal/checkers/checkjs/httpapi.go` | `http.get/post/put/patch/delete/head` convenience API |
| `back/internal/checkers/checkjs/errors.go` | Custom error types (`ErrMaxSubChecks`, etc.) |
| `back/internal/checkers/checkjs/samples.go` | Sample configurations |
| `back/internal/checkers/checkjs/checker_test.go` | Table-driven tests |

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeJS CheckType = "js"` |
| `back/internal/checkers/registry/registry.go` | Add `checkjs` cases to `GetChecker()` and `ParseConfig()` |
| `back/go.mod` | Add `github.com/dop251/goja` |

### Execution flow (`checker.go`)

```go
func (c *JSChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
    cfg := config.(*JSConfig)
    timeout := cfg.Timeout
    if timeout == 0 {
        timeout = defaultTimeout
    }
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    start := time.Now()

    // Wrap script in function body
    wrapped := "(function() {\n" + cfg.Script + "\n})()"
    program, err := goja.Compile("check.js", wrapped, false)
    if err != nil {
        return errorResult("script compilation failed: " + err.Error()), nil
    }

    // Create sandboxed runtime
    bridge := newCheckerBridge(ctx, maxSubChecks)
    vm, console := newSandboxedRuntime(ctx, cfg, bridge)

    // Execute
    val, err := vm.RunProgram(program)
    duration := time.Since(start)

    if err != nil {
        if isContextCancelled(ctx) {
            return timeoutResult(duration, console), nil
        }
        return scriptErrorResult(err, duration, console), nil
    }

    return mapScriptResult(val, duration, console, bridge.subCheckCount), nil
}
```

### Runtime setup (`runtime.go`)

```go
func newSandboxedRuntime(ctx context.Context, cfg *JSConfig, bridge *CheckerBridge) (*goja.Runtime, *consoleCapture) {
    vm := goja.New()
    vm.SetMaxCallStackSize(1024)

    // Context cancellation → VM interrupt
    go func() {
        <-ctx.Done()
        vm.Interrupt("execution timeout")
    }()

    // Inject globals
    console := newConsoleCapture(maxConsoleSize)
    vm.Set("console", console.toObject(vm))
    vm.Set("env", cfg.Env)
    vm.Set("solidping", bridge.toObject(vm))
    vm.Set("http", bridge.httpAPI(vm))
    vm.Set("sleep", bridge.sleepFunc(ctx))

    return vm, console
}
```

### Checker bridge (`bridge.go`)

```go
type CheckerBridge struct {
    ctx           context.Context
    subCheckCount int
    maxSubChecks  int // 20
}

func (b *CheckerBridge) check(checkType string, configMap map[string]any) map[string]any {
    // Block recursion and passive checks
    if checkType == "js" || checkType == "heartbeat" {
        panic(b.vm.NewGoError(fmt.Errorf("check type %q cannot be called from scripts", checkType)))
    }

    // Enforce sub-check limit
    if b.subCheckCount >= b.maxSubChecks {
        panic(b.vm.NewGoError(fmt.Errorf("maximum sub-checks (%d) exceeded", b.maxSubChecks)))
    }
    b.subCheckCount++

    // Look up checker
    checker, ok := registry.GetChecker(checkerdef.CheckType(checkType))
    if !ok {
        panic(b.vm.NewGoError(fmt.Errorf("unknown check type: %s", checkType)))
    }

    // Parse and validate config
    config, _ := registry.ParseConfig(checkerdef.CheckType(checkType))
    if err := config.FromMap(configMap); err != nil {
        panic(b.vm.NewGoError(fmt.Errorf("invalid config: %w", err)))
    }

    // Execute within parent context
    result, err := checker.Execute(b.ctx, config)
    if err != nil {
        return map[string]any{"status": "error", "output": map[string]any{"error": err.Error()}}
    }

    return map[string]any{
        "status":   result.Status.String(),
        "duration": float64(result.Duration.Microseconds()) / 1000.0,
        "metrics":  result.Metrics,
        "output":   result.Output,
    }
}
```

### HTTP convenience API (`httpapi.go`)

Uses `net/http` directly (not the HTTP checker). Respects parent context timeout. Response body capped at 1MB. Counts toward sub-check limit.

```go
func (b *CheckerBridge) httpRequest(method, url string, opts map[string]any) map[string]any {
    // Build request with method, url, headers, body from opts
    // Execute with parent context
    // Return {statusCode, body, headers, duration}
}
```

### Console capture (`runtime.go`)

```go
type consoleCapture struct {
    buf     strings.Builder
    maxSize int
    muted   bool
}

func (c *consoleCapture) log(level string, args ...goja.Value) {
    if c.muted { return }
    line := fmt.Sprintf("[%s] %s", level, formatArgs(args))
    if c.buf.Len()+len(line) > c.maxSize {
        c.muted = true
        c.buf.WriteString("\n[console output truncated at 16KB]")
        return
    }
    c.buf.WriteString(line)
    c.buf.WriteByte('\n')
}
```

---

## Security Constraints

| Constraint | Limit | Enforcement |
|---|---|---|
| Script size | 64KB | Validated in `Validate()` |
| Timeout | Max 60s, default 30s | `context.WithTimeout` + `vm.Interrupt` |
| Sub-checks | Max 20 per execution | Counter in bridge, panics after limit |
| Console output | 16KB | `consoleCapture` truncates at limit |
| HTTP response body | 1MB | `io.LimitReader` in `httpapi.go` |
| Stack depth | 1024 frames | `vm.SetMaxCallStackSize(1024)` |
| Network access | Only through bridge and HTTP API | goja has no native network APIs |
| Filesystem | None | goja has no native FS APIs |
| Process spawning | None | goja has no native process APIs |
| Module loading | None | No `require`, no `import` |
| Recursion | `js` and `heartbeat` types blocked in bridge | Type check before execution |
| Memory | No hard limit (goja limitation) | Timeout is the safety valve — same approach as k6 |

---

## Dashboard UI

### Check Form (`check-form.tsx`)

Add `case "js"` to `renderConfigFields()`:

**Script editor:**
- `<textarea>` with `font-mono text-sm`, minimum height 300px
- Placeholder showing a simple example script
- `data-testid="check-script-input"`

**Environment variables section** (collapsible `Card` labeled "Environment Variables"):
- Key-value pair editor
- "Add variable" button appends a row with two inputs
- Value inputs use `type="password"` with show/hide toggle
- Trash icon to remove a row
- `data-testid="check-env-vars"`

### Check Type Entry

```typescript
{ value: "js", label: "JavaScript", description: "Custom monitoring scripts" }
```

### Check List Target Display

```typescript
check.type === "js"
  ? "Script"
  : // ... existing logic
```

### Check Detail Page (`checks.$checkUid.index.tsx`)

When check type is `js`, the existing generic key-value display shows metrics and output. For better readability:
- `console` output field displayed in a `<pre>` block with monospace font
- `subChecks` displayed as a count badge

### Translations

Add to `apps/dash0/src/locales/en/checks.json`:

```json
{
  "jsScript": "Script",
  "jsScriptPlaceholder": "Write your monitoring script...",
  "jsEnvVars": "Environment Variables",
  "jsAddEnvVar": "Add Variable",
  "jsEnvKey": "Key",
  "jsEnvValue": "Value",
  "jsConsoleOutput": "Console Output",
  "jsSubChecks": "Sub-checks executed"
}
```

Add French translations to `fr/checks.json`.

---

## Tests

### Config tests

| Test case | Description |
|-----------|-------------|
| Full config | Script + timeout + env -> correct JSConfig |
| Minimal config | Script only -> defaults applied |
| Timeout parsing | `"15s"` -> 15 * time.Second |
| Missing script | -> `ConfigError{Parameter: "script"}` |
| Script too large | > 64KB -> validation error |
| Invalid timeout | > 60s, negative -> validation error |
| Too many env vars | > 50 entries -> validation error |
| Env key too long | > 128 chars -> validation error |
| Syntax error in script | -> validation error with line info |
| GetConfig roundtrip | FromMap -> GetConfig -> FromMap produces same result |

### Execution tests

| Test case | Description |
|-----------|-------------|
| Return up | `return {status: "up"}` -> StatusUp |
| Return down with output | `return {status: "down", output: {error: "test"}}` -> StatusDown |
| Return with metrics | `return {status: "up", metrics: {foo: 42}}` -> metrics populated |
| Script throws | `throw new Error("boom")` -> StatusError with error message |
| Infinite loop | `while(true){}` with short timeout -> StatusTimeout |
| No return | Empty script -> StatusError |
| Invalid status string | `return {status: "banana"}` -> StatusError |
| Missing status field | `return {metrics: {}}` -> StatusError |
| Console capture | `console.log("hello")` -> output.console contains "hello" |
| Console truncation | Large output -> truncated at 16KB |
| Env access | Script reads `env.MY_KEY` -> correct value |
| Env missing key | Script reads `env.MISSING` -> undefined |
| Sleep | `sleep(100); return {status: "up"}` -> duration >= 100ms |
| Stack overflow | Deep recursion -> error, not crash |

### Bridge tests (using httptest server)

| Test case | Description |
|-----------|-------------|
| `solidping.http()` | Calls HTTP checker against httptest, returns result |
| `solidping.tcp()` | Calls TCP checker, returns result |
| `solidping.check("http", ...)` | Generic call works |
| `solidping.check("js", ...)` | Blocked -> JS exception |
| `solidping.check("heartbeat", ...)` | Blocked -> JS exception |
| `solidping.check("unknown", ...)` | Unknown type -> JS exception |
| Sub-check limit | 21 calls -> error on 21st |
| `http.get()` | Makes request, returns `{statusCode, body, headers, duration}` |
| `http.post()` with body | Sends body correctly |
| `http.get()` large response | Body truncated at 1MB |

### Dashboard E2E (Playwright)

- Create JS check with script and env vars -> verify saved in config
- Edit existing check -> modify script -> verify update
- Check detail page -> verify console output display

---

## Sample Configurations (`samples.go`)

### Multi-endpoint health check

```json
{
  "type": "js",
  "name": "API + DB Health",
  "config": {
    "script": "var api = solidping.http({url: 'https://api.example.com/health'});\nvar db = solidping.tcp({host: 'db.example.com', port: 5432});\n\nvar down = [];\nif (api.status !== 'up') down.push('api');\nif (db.status !== 'up') down.push('database');\n\nreturn {\n  status: down.length === 0 ? 'up' : 'down',\n  metrics: {apiTime: api.metrics.duration_ms, dbTime: db.metrics.duration_ms},\n  output: {downServices: down}\n};"
  }
}
```

### API response validation with business logic

```json
{
  "type": "js",
  "name": "Queue Depth Monitor",
  "config": {
    "script": "var resp = http.get('https://api.example.com/v1/queue/stats', {\n  headers: {'Authorization': 'Bearer ' + env.API_KEY}\n});\n\nif (resp.statusCode !== 200) {\n  return {status: 'down', output: {error: 'API returned ' + resp.statusCode}};\n}\n\nvar data = JSON.parse(resp.body);\nvar depth = data.pending || 0;\nvar consumers = data.consumers || 0;\n\nif (depth > 10000 && consumers === 0) {\n  return {status: 'down', metrics: {queueDepth: depth}, output: {error: 'Queue backing up with no consumers'}};\n}\n\nreturn {status: 'up', metrics: {queueDepth: depth, consumers: consumers}};",
    "env": {
      "API_KEY": "sk-xxx"
    }
  }
}
```

### Sequential dependency chain

```json
{
  "type": "js",
  "name": "DNS -> SSL -> API Chain",
  "config": {
    "script": "var dns = solidping.dns({domain: 'api.example.com'});\nif (dns.status !== 'up') {\n  return {status: 'down', output: {error: 'DNS resolution failed'}};\n}\n\nvar ssl = solidping.ssl({host: 'api.example.com'});\nif (ssl.status !== 'up') {\n  return {status: 'down', output: {error: 'SSL issue', detail: ssl.output}};\n}\n\nvar api = solidping.http({url: 'https://api.example.com/health'});\nreturn {\n  status: api.status,\n  metrics: {dnsTime: dns.metrics.duration_ms, apiTime: api.metrics.duration_ms}\n};"
  }
}
```

### Multi-region comparison

```json
{
  "type": "js",
  "name": "Multi-Region API Check",
  "config": {
    "script": "var regions = [\n  {name: 'us-east', url: 'https://us-east.api.example.com/health'},\n  {name: 'eu-west', url: 'https://eu-west.api.example.com/health'},\n  {name: 'ap-south', url: 'https://ap-south.api.example.com/health'}\n];\n\nvar results = [];\nvar downCount = 0;\nfor (var i = 0; i < regions.length; i++) {\n  var r = solidping.http({url: regions[i].url});\n  results.push({region: regions[i].name, status: r.status, time: r.metrics.duration_ms});\n  if (r.status !== 'up') downCount++;\n}\n\nreturn {\n  status: downCount === 0 ? 'up' : 'down',\n  metrics: {downRegions: downCount, totalRegions: regions.length},\n  output: {results: results}\n};"
  }
}
```

---

## Implementation Order

1. **Core runtime** — `config.go`, `errors.go`, `checker.go`, `runtime.go`. Add `CheckTypeJS` to types and registry. Add goja dependency. Test: simple scripts without sub-checks.
2. **Checker bridge** — `bridge.go` with `solidping.check()` and typed wrappers. Test: scripts calling sub-checkers.
3. **HTTP API** — `httpapi.go` with `http.get/post/etc`. Test: scripts making HTTP requests.
4. **Samples** — `samples.go` with demo configurations.
5. **Dashboard** — Script editor, env var editor, check-form integration, translations, E2E tests.

---

## Security Considerations

- **Resource exhaustion**: goja has no built-in memory limit. The primary defense is the context timeout (30s default, 60s max). A script allocating a huge array will succeed if it does so within the timeout. This is the same practical approach k6 uses. Future mitigation: run JS checks in a separate worker pool with per-process memory limits.
- **Sub-check recursion**: JS checks cannot call other JS checks (blocked in bridge). The 20 sub-check limit and context timeout provide additional bounds.
- **Secret exposure**: `env` values are stored in the JSONB `config` column with the same access controls as other check configs (per-organization). The dashboard should use password-type inputs for env values and consider redacting them in GET responses.
- **Error message leakage**: goja error messages may include script source. Error output is stored in the check's result, which is already organization-scoped.
- **goja ES6 limitations**: No async/await, no Promises, no generators, no classes, no modules, no Map/Set. Scripts must be synchronous. This must be clearly documented in the dashboard UI (placeholder text, help link).

---

## Competitor Reference

- **Checkly**: API check scripts in Node.js with full runtime; browser checks use Playwright. Our approach is more lightweight (no full Node.js) but supports checker composition.
- **Grafana Synthetic Monitoring**: Uses k6 scripts powered by goja — same engine we're using. Validates the approach.
- **Uptime Kuma**: No scripting capability. JSON Query monitor is the closest.
- **Gatus**: No scripting. Conditions are declarative only.
- **Datadog Synthetic Monitoring**: Multi-step API tests with assertions. Declarative, not scriptable.

SolidPing's JS checker differentiator: **sub-check composition**. No competitor exposes their existing protocol checkers as callable functions within scripts.
