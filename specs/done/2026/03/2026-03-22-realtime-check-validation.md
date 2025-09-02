# Real-Time Server-Side Check Parameter Validation

## Overview

Add a server-side validation endpoint for check configurations, and call it from the frontend with a 1s debounce as the user edits any check parameter. Add Goja-based JavaScript syntax validation in the `js` checker.

Currently, validation only happens on form submit — users get no feedback until they click "Create Check" or "Save Changes", and the only JS validation is size/timeout bounds checking.

## Motivation

1. **Late error discovery**: Users fill out an entire form only to learn on submit that a field is invalid (e.g., invalid URL, port out of range, bad regex pattern).
2. **No JS syntax feedback**: The JavaScript check accepts any text — users only discover syntax errors when the check runs for the first time and fails.
3. **Single source of truth**: By validating server-side, we guarantee the exact same rules apply during real-time feedback and on submit. No risk of frontend/backend drift. The backend already has thorough per-field validation in each checker's `Validate()` and `FromMap()` methods — we just need to expose it.

## Current State

**Frontend** (`apps/dash0/src/components/shared/check-form.tsx`):
- Single `error` state (string) set only on submit or API error response
- Validation in `handleSubmit` only checks required fields (e.g., "URL is required")
- No per-field error display — only a single `<Alert>` banner at the top
- Existing `useDebounce` hook in `checks.index.tsx` (300ms for search)

**Backend validation** (per checker `config.go` + `Validate()`):
- Each checker implements `Validate(spec *CheckSpec) error`
- `Config.FromMap(map)` does type coercion and basic parsing
- Errors are returned as `checkerdef.ConfigError{Parameter, Message}` — already structured for per-field display
- Handler already translates `ConfigError` into `422 Unprocessable Entity` with `{title, code, fields: [{name, message}]}` via `WriteValidationError`

**Backend JS checker** (`back/internal/checkers/checkjs/`):
- `JSConfig.Validate()` checks: script required, max 64KB, timeout bounds, env entry count
- No syntax validation — script is only compiled at execution time via `goja.Runtime.RunString()`

---

## 1. Backend: Validation Endpoint

### New Route

```
POST /api/v1/orgs/:org/checks/validate
```

Registered in `server.go` alongside existing check routes, behind `authMiddleware.RequireAuth`.

### Request Body

Same shape as `CreateCheckRequest` — `type` + `config` are required, other fields optional:

```json
{
  "type": "http",
  "config": {
    "url": "not-a-url",
    "expectedStatus": 999
  }
}
```

### Response

**Valid config** — `200 OK`:
```json
{
  "valid": true
}
```

**Invalid config** — `200 OK` (not 422, since this is an expected "query" result, not a failed mutation):
```json
{
  "valid": false,
  "fields": [
    { "name": "url", "message": "must be a valid HTTP or HTTPS URL" },
    { "name": "expectedStatus", "message": "must be between 100 and 599" }
  ]
}
```

Using 200 for both cases is deliberate: the validate endpoint is a query ("is this config valid?"), not an action. A 422 would make the frontend error-handling path treat validation feedback as a request failure.

**Actual errors** (bad JSON, missing type, unknown type) — use standard error codes (400/422).

### Implementation

**File**: `back/internal/handlers/checks/handler.go`

Add `ValidateCheck` handler method:

```go
// ValidateCheck validates a check configuration without creating or updating the check.
func (h *Handler) ValidateCheck(writer http.ResponseWriter, req bunrouter.Request) error {
	var validateReq ValidateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&validateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	result, err := h.svc.ValidateCheck(req.Context(), validateReq)
	if err != nil {
		return h.handleValidateError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}
```

**File**: `back/internal/handlers/checks/service.go`

Add `ValidateCheck` service method. This reuses the exact same `registry.GetChecker` + `checker.Validate(spec)` path used by `CreateCheck`, but without touching the database:

```go
// ValidateCheckRequest is the request body for validate.
type ValidateCheckRequest struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// ValidateCheckResponse is the response for validate.
type ValidateCheckResponse struct {
	Valid  bool                       `json:"valid"`
	Fields []base.ValidationErrorField `json:"fields,omitempty"`
}

// ValidateCheck validates a check configuration without persisting.
func (s *Service) ValidateCheck(_ context.Context, req ValidateCheckRequest) (ValidateCheckResponse, error) {
	if req.Type == "" {
		return ValidateCheckResponse{}, ErrInvalidCheckType
	}

	checker, ok := registry.GetChecker(checkerdef.CheckType(req.Type))
	if !ok {
		return ValidateCheckResponse{}, ErrInvalidCheckType
	}

	spec := &checkerdef.CheckSpec{
		Config: req.Config,
	}

	if err := checker.Validate(spec); err != nil {
		if configErr := checkerdef.IsConfigError(err); configErr != nil {
			return ValidateCheckResponse{
				Valid: false,
				Fields: []base.ValidationErrorField{
					{Name: configErr.Parameter, Message: configErr.Message},
				},
			}, nil
		}
		// Non-config error (unexpected) — return as a generic field error
		return ValidateCheckResponse{
			Valid:  false,
			Fields: []base.ValidationErrorField{{Name: "_", Message: err.Error()}},
		}, nil
	}

	return ValidateCheckResponse{Valid: true}, nil
}
```

**Route registration** in `server.go`:
```go
orgChecks.POST("/validate", checksHandler.ValidateCheck)
```

> **Important**: register `/validate` **before** `/:checkUid` so bunrouter matches the literal path first.

### Multi-field errors

Today, `Validate()` returns on the first error. This is fine for the initial implementation — the user fixes one field, the next error surfaces 1s later. Future improvement: collect all errors (not in scope here).

---

## 2. Backend: JS Syntax Validation with Goja

### Problem

`JSConfig.Validate()` checks size/timeout bounds but does not compile the script. A script like `function(` passes validation, then fails at execution time with a cryptic Goja error.

### Change

**File**: `back/internal/checkers/checkjs/config.go`

Add Goja compilation step to `Validate()`, after the existing checks:

```go
func (c *JSConfig) Validate() error {
	// ... existing checks (required, size, timeout, env count) ...

	// Syntax validation: compile without executing
	wrapped := "(function() {\n" + c.Script + "\n})()"
	if _, err := goja.Compile("script", wrapped, true); err != nil {
		return checkerdef.NewConfigError("script", "syntax error: "+err.Error())
	}

	return nil
}
```

This uses `goja.Compile()` which parses and compiles the script to bytecode without executing it. The wrapping matches what `Execute()` does (`"(function() {\n" + script + "\n})()"`) so syntax errors are reported in the same context.

### Why Goja and not `new Function()`

The browser's JS engine (V8/SpiderMonkey) and Goja (ES5.1 + partial ES6) have different syntax support. For example:
- Arrow functions (`=>`) — supported in V8, not in Goja
- `async/await` — supported in V8, not in Goja
- Template literals — supported in V8, partial in Goja

Using Goja server-side gives the user the **exact same** error they would get at execution time.

---

## 3. Frontend: JS Script Editor with Syntax Highlighting

### Problem

The JS check script field is a plain `<Textarea>` with no syntax highlighting. Writing and debugging JavaScript in a monospace text box without visual cues is painful, especially when combined with deferred validation errors.

### Approach

Use [CodeMirror 6](https://codemirror.net/) via the `@uiw/react-codemirror` React wrapper. CodeMirror is the standard choice for in-browser code editors — lightweight, tree-shakeable, and supports JavaScript syntax highlighting out of the box.

### Dependencies

```bash
cd apps/dash0 && bun add @uiw/react-codemirror @codemirror/lang-javascript
```

### Component

Replace the `<Textarea>` in the `js` case of `renderConfigFields()` with a CodeMirror editor:

```tsx
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";

// In the "js" case of renderConfigFields():
case "js":
  return (
    <div className="space-y-2">
      <Label htmlFor="script">Script</Label>
      <CodeMirror
        value={script}
        onChange={(value) => setScript(value)}
        extensions={[javascript()]}
        height="200px"
        className={cn(
          "rounded-md border text-sm",
          getFieldError(fieldErrors, "script") && "border-destructive"
        )}
        data-testid="check-script"
      />
      {getFieldError(fieldErrors, "script") && (
        <p className="text-xs text-destructive">
          {getFieldError(fieldErrors, "script")}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        JavaScript script that returns an object with status ("up", "down", or "error"),
        optional metrics, and optional output.
      </p>
    </div>
  );
```

### Theme

Use the default CodeMirror theme, which adapts to light/dark mode via CSS variables. If needed, pass `theme="dark"` conditionally based on the app's current theme.

### Scope

Syntax highlighting only — no autocompletion, no linting extensions. Keep the bundle impact minimal. The server-side validation via the debounced API call handles error feedback.

---

## 4. Frontend: Debounced Validation API Call

### New Hook: `useCheckValidation`

**File**: `apps/dash0/src/hooks/use-check-validation.ts`

```typescript
import { useState, useEffect, useRef } from "react";
import { apiClient } from "@/api/client";

export interface FieldError {
  name: string;
  message: string;
}

export function useCheckValidation(
  org: string,
  type: string | undefined,
  config: Record<string, unknown>,
  debounceMs = 1000
): FieldError[] {
  const [errors, setErrors] = useState<FieldError[]>([]);
  const isFirstRender = useRef(true);

  // Serialize config to a stable string for dependency tracking
  const configKey = JSON.stringify(config);

  useEffect(() => {
    if (isFirstRender.current) {
      isFirstRender.current = false;
      return;
    }

    if (!type) return;

    const timer = setTimeout(async () => {
      try {
        const res = await apiClient(`/api/v1/orgs/${org}/checks/validate`, {
          method: "POST",
          body: JSON.stringify({ type, config }),
        });
        const data = await res.json();
        setErrors(data.valid ? [] : (data.fields ?? []));
      } catch {
        // Network error or 4xx/5xx — don't show validation errors,
        // the submit handler will catch real issues
        setErrors([]);
      }
    }, debounceMs);

    return () => clearTimeout(timer);
  }, [org, type, configKey, debounceMs]); // eslint-disable-line react-hooks/exhaustive-deps

  return errors;
}
```

### Helper: Get field error

```typescript
export function getFieldError(errors: FieldError[], name: string): string | undefined {
  return errors.find((e) => e.name === name)?.message;
}
```

### Integrate into CheckForm

**File**: `apps/dash0/src/components/shared/check-form.tsx`

1. Build a `config` object reactively from the current form state (same logic as `handleSubmit` but without the required-field guards — send whatever we have)
2. Call `useCheckValidation(org, type, config, 1000)`
3. Display per-field errors below each input

**Config building** (new `useMemo`):
```tsx
const currentConfig = useMemo(() => {
  const cfg: Record<string, unknown> = {};
  switch (type) {
    case "http":
    case "ssl":
    case "websocket":
      if (url) cfg.url = url;
      if (type === "http") {
        if (method) cfg.method = method;
        if (expectedStatus) cfg.expectedStatus = parseInt(expectedStatus, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
      }
      break;
    case "tcp": case "udp": case "ftp": case "sftp":
    case "ssh": case "pop3": case "imap": case "smtp":
    case "postgresql":
      if (host) cfg.host = host;
      if (port) cfg.port = parseInt(port, 10);
      if (username) cfg.username = username;
      if (password) cfg.password = password;
      if (type === "smtp") {
        if (startTLS) cfg.starttls = true;
        if (tlsVerify) cfg.tls_verify = true;
        if (ehloDomain) cfg.ehlo_domain = ehloDomain;
        if (expectGreeting) cfg.expect_greeting = expectGreeting;
        if (checkAuth) cfg.check_auth = true;
      }
      if (type === "postgresql") {
        if (database) cfg.database = database;
        if (query) cfg.query = query;
      }
      break;
    case "icmp":
      if (host) cfg.host = host;
      break;
    case "dns": case "domain":
      if (domain) cfg.domain = domain;
      break;
    case "js":
      if (script) cfg.script = script;
      break;
  }
  return cfg;
}, [type, url, method, expectedStatus, host, port, domain, script,
    username, password, startTLS, tlsVerify, ehloDomain, expectGreeting,
    checkAuth, database, query]);

const fieldErrors = useCheckValidation(org, type, currentConfig, 1000);
```

**Per-field error display** (example for URL):
```tsx
<Input
  id="url"
  type="url"
  value={url}
  onChange={(e) => setUrl(e.target.value)}
  className={cn("flex-1", getFieldError(fieldErrors, "url") && "border-destructive")}
/>
{getFieldError(fieldErrors, "url") && (
  <p className="text-xs text-destructive">{getFieldError(fieldErrors, "url")}</p>
)}
```

Apply the same pattern to every config field: `host`, `port`, `domain`, `script`, `expectedStatus`, `username` (where required), etc.

**On submit**: Keep the existing `handleSubmit` logic. The server-side validation endpoint is only for real-time feedback — the create/update endpoints still validate and return errors independently. The submit button should be disabled when `fieldErrors.length > 0` to prevent submitting known-invalid configs.

### Skip validation when config is empty

Don't call the validate endpoint when the config object is empty (e.g., right after switching check type). This avoids a flurry of "field X is required" errors before the user starts typing.

---

## 5. Implementation Steps

1. **Add `ValidateCheck` to backend** — request/response types, service method, handler, route registration
2. **Add Goja syntax validation to `JSConfig.Validate()`** — `goja.Compile()` call after existing checks
3. **Write backend tests** — test the validate endpoint for each check type (valid + invalid), test JS syntax errors
4. **Add CodeMirror for JS script field** — install deps, replace `<Textarea>` with CodeMirror + `javascript()` extension
5. **Create `useCheckValidation` hook** — debounced API call with per-field error array
6. **Update `check-form.tsx`** — build `currentConfig` memo, wire up the hook, add per-field error display to all fields
7. **E2E tests** — Playwright tests for real-time validation feedback and JS editor

## Testing Strategy

### Backend Unit Tests

**File**: `back/internal/handlers/checks/handler_test.go` (or new `validate_test.go`)

Table-driven tests for `POST /api/v1/orgs/:org/checks/validate`:

| Type | Config | Expected `valid` | Expected field |
|------|--------|-------------------|----------------|
| `http` | `{"url": "https://example.com"}` | `true` | — |
| `http` | `{"url": "not-a-url"}` | `false` | `url` |
| `http` | `{"url": "https://x.com", "expectedStatus": 999}` | `false` | `expectedStatus` |
| `tcp` | `{}` | `false` | `host` |
| `tcp` | `{"host": "example.com", "port": 80}` | `true` | — |
| `js` | `{"script": "return { status: 'up' }"}` | `true` | — |
| `js` | `{"script": "function("}` | `false` | `script` (syntax error) |
| `js` | `{"script": ""}` | `false` | `script` (required) |
| `dns` | `{"domain": "example.com"}` | `true` | — |
| `postgresql` | `{"host": "db", "username": "pg"}` | `true` | — |
| `postgresql` | `{"host": "db"}` | `false` | `username` |
| (empty) | `{}` | 400 | invalid type |

### JS Syntax Validation Unit Tests

**File**: `back/internal/checkers/checkjs/checker_test.go`

Add cases to `TestJSChecker_Validate`:

| Script | Expected |
|--------|----------|
| `return { status: "up" }` | pass |
| `function(` | fail — syntax error |
| `var x = {` | fail — unterminated |
| `console.log("hello")` | pass |
| `() => { return 1 }` | fail — arrow functions not supported in Goja |

### E2E Tests (Playwright)

- Create HTTP check, type invalid URL → wait 1s → verify red border + error message below URL field
- Fix the URL → wait 1s → verify error disappears
- Create JS check, type `function(` → wait 1s → verify syntax error message appears below script textarea
- Fix the script → wait 1s → verify error disappears
- Submit with valid config → verify check is created (no regressions)
- Verify no validation request fires on initial form load (no flash of errors)

### Manual Testing Checklist

- [ ] Each check type: leave required field empty, wait 1s → error appears
- [ ] HTTP: enter `not-a-url` → "must be a valid HTTP or HTTPS URL"
- [ ] HTTP: status 999 → "must be between 100 and 599"
- [ ] JS: `function(` → syntax error with Goja message
- [ ] JS: arrow function `() => 1` → error (Goja doesn't support)
- [ ] JS: valid script → no error
- [ ] TCP: port 99999 → port range error
- [ ] Fix any error → error clears after 1s
- [ ] Submit with errors → button disabled
- [ ] Network offline → no false validation errors shown
- [ ] Rapid typing → only one validation call after 1s of inactivity
- [ ] JS script editor shows syntax highlighting (keywords, strings, comments in distinct colors)
- [ ] JS script editor respects light/dark theme
