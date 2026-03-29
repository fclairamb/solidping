# Headless Browser Monitoring

## Overview

Add a headless Chrome/Chromium-based health check that loads a web page in a real browser and validates it rendered correctly. Unlike the HTTP check which only validates status codes and response bodies, this check executes JavaScript, renders CSS, loads all sub-resources, and can take screenshots — catching issues that server-side checks miss.

**Use cases:**
- Verify a page loads completely (not just returns 200) — catches JS errors, broken CSS, missing assets
- Detect client-side rendering failures in SPAs (React, Vue, Angular)
- Validate that login pages, dashboards, or checkout flows actually render
- Take periodic screenshots for visual regression monitoring
- Measure real browser performance metrics (First Contentful Paint, DOM Content Loaded)
- Detect mixed content, CSP violations, or certificate warnings visible only in browsers

## Check Type
Type: `browser`

---

## Backend

### Package: `server/internal/checkers/checkbrowser/`

| File | Description |
|------|-------------|
| `config.go` | `BrowserConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `BrowserChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests |

### Configuration (`BrowserConfig`)

```go
type BrowserConfig struct {
    URL              string        `json:"url"`
    WaitSelector     string        `json:"wait_selector,omitempty"`
    Keyword          string        `json:"keyword,omitempty"`
    InvertKeyword    bool          `json:"invert_keyword,omitempty"`
    Screenshot       bool          `json:"screenshot,omitempty"`
    ScreenshotDelay  time.Duration `json:"screenshot_delay,omitempty"`
    Timeout          time.Duration `json:"timeout,omitempty"`
    AcceptedStatuses []int         `json:"accepted_statuses,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | **yes** | — | URL to load (must be `http://` or `https://`) |
| `wait_selector` | string | no | — | CSS selector to wait for before declaring success |
| `keyword` | string | no | — | Text to search for in the rendered page body |
| `invert_keyword` | bool | no | `false` | If true, check that keyword is NOT present |
| `screenshot` | bool | no | `false` | Take a screenshot after page load |
| `screenshot_delay` | duration | no | `0s` | Wait before taking screenshot (for animations) |
| `timeout` | duration | no | `30s` | Maximum time for page load |
| `accepted_statuses` | int[] | no | `[200-399]` | HTTP status codes considered successful |

### Validation Rules

- `url` is required and must start with `http://` or `https://` (reject `file://`, `data:`, `javascript:`)
- `wait_selector` must be a valid CSS selector (basic format check)
- `timeout` must be > 0 and ≤ 120s (browser checks are inherently slower)
- `screenshot_delay` must be ≤ 30s
- Auto-generate `spec.Name` from URL hostname if empty
- Auto-generate `spec.Slug` as `browser-{hostname}` if empty

### Execution Behavior

1. Allocate a browser context from the chromedp pool (reuse browser process)
2. Create a new tab with timeout context
3. Record `t0` — navigate to URL
4. Wait for network idle (no new requests for 500ms) or `wait_selector` if configured
5. Record `t1` — compute `load_time_ms`
6. Capture page metrics:
   - HTTP status code from navigation response
   - DOM Content Loaded timing
   - First Contentful Paint timing
   - Console error count
7. If `keyword` set, search rendered page text for keyword
8. If `screenshot` enabled, wait `screenshot_delay` then capture PNG screenshot
9. Store screenshot as base64 in output (or save to temp file with reference)
10. Close tab
11. Return result

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Page loads + status in accepted range (+ keyword found if set) | `StatusUp` |
| Navigation failure (DNS, connection refused) | `StatusDown` |
| HTTP status not in accepted range | `StatusDown` |
| Keyword not found in page (or found when inverted) | `StatusDown` |
| `wait_selector` element never appears | `StatusDown` |
| Page load timeout | `StatusTimeout` |
| Browser crash / chromedp error | `StatusError` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `load_time_ms` | float64 | Time to full page load (network idle) |
| `dom_content_loaded_ms` | float64 | DOM Content Loaded event timing |
| `first_contentful_paint_ms` | float64 | First Contentful Paint timing |
| `total_time_ms` | float64 | Total check duration |
| `console_errors` | int | Number of JS console errors |
| `resource_count` | int | Number of sub-resources loaded |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `url` | string | Loaded URL (after redirects) |
| `status_code` | int | HTTP response status code |
| `title` | string | Page `<title>` content |
| `keyword_found` | bool | Whether keyword was found |
| `screenshot_path` | string | Path to screenshot file (if enabled) |
| `console_errors` | []string | JS console error messages (first 5) |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/chromedp/chromedp` (Chrome DevTools Protocol for Go). Add to `server/go.mod`.

```go
import (
    "github.com/chromedp/chromedp"
    "github.com/chromedp/cdproto/page"
    "github.com/chromedp/cdproto/runtime"
)
```

### Browser Lifecycle Management

The browser process should be managed at the worker level, not per-check:

```go
// Global browser allocator (one per worker process)
var (
    browserCtx    context.Context
    browserCancel context.CancelFunc
    browserOnce   sync.Once
)

func getBrowserContext() context.Context {
    browserOnce.Do(func() {
        opts := append(chromedp.DefaultExecAllocatorOptions[:],
            chromedp.Flag("headless", true),
            chromedp.Flag("disable-gpu", true),
            chromedp.Flag("no-sandbox", true),
            chromedp.Flag("disable-dev-shm-usage", true),
        )
        browserCtx, browserCancel = chromedp.NewExecAllocator(context.Background(), opts...)
    })
    return browserCtx
}
```

Each check creates a new tab (context) from the shared browser, ensuring isolation without the overhead of launching a new browser process per check.

### Security Considerations

- **URL validation**: Strictly reject `file://`, `data:`, `javascript:` URLs to prevent SSRF and LFI
- **Network isolation**: Consider restricting the browser to external networks only (no localhost/internal)
- **Resource limits**: Set memory and CPU limits on the browser process
- **Screenshot storage**: Screenshots may contain sensitive data — handle accordingly
- **Sandbox**: Always run with `--no-sandbox` in containers but document the trade-off
- **Chrome binary**: Require Chromium/Chrome to be installed on the worker host

### Prerequisites

Unlike other checkers, the browser checker requires **Chromium or Google Chrome** to be installed on the worker host. This should be:
- Documented in the README
- Detected at startup with a clear error message if missing
- Optional: provide a Docker image variant with Chromium pre-installed

### Testing

**Test cases** (table-driven):
1. **Happy path** — load a simple HTML page, expect `StatusUp`
2. **Keyword found** — page contains expected text, expect `StatusUp`
3. **Keyword not found** — expect `StatusDown`
4. **Wait for selector** — wait for `#app` element, expect `StatusUp`
5. **Selector not found** — timeout waiting for element, expect `StatusDown`
6. **Screenshot** — verify screenshot file is created
7. **Bad URL** — non-existent domain, expect `StatusDown`
8. **HTTP 500** — server error, expect `StatusDown`
9. **Page timeout** — very slow page, expect `StatusTimeout`
10. **Invalid URL scheme** — `file://`, expect validation error
11. **SPA rendering** — page with client-side rendering, verify content

### Limitations

- Requires Chrome/Chromium installed on worker host
- Higher resource consumption than other check types (browser process)
- Not suitable for sub-minute check intervals (browser startup overhead)
- Screenshots consume storage and bandwidth
- No cookie/session management (each check is a fresh incognito context)
- No multi-step flows (login → navigate → check) in initial implementation
- No visual diff comparison (only text-based checks)
- HTTP/2 push and Service Worker behavior may differ from real user sessions

### Future Enhancements

- Multi-step navigation flows (login, click, assert)
- Visual regression comparison (pixel diff against baseline screenshot)
- HAR file export for performance analysis
- Cookie injection for authenticated page checks
- Custom JavaScript execution before/after page load
- Lighthouse score integration

---

## Frontend

### Form Fields

```tsx
case "browser":
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="url">URL</Label>
        <Input id="url" type="url" placeholder="https://example.com"
          value={url} onChange={(e) => setUrl(e.target.value)} />
      </div>
      <div className="space-y-2">
        <Label htmlFor="waitSelector">Wait for Selector (optional)</Label>
        <Input id="waitSelector" type="text" placeholder="#app, .content, [data-loaded]"
          value={waitSelector} onChange={(e) => setWaitSelector(e.target.value)} />
        <p className="text-xs text-muted-foreground">
          CSS selector to wait for before declaring the page loaded
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="keyword">Keyword (optional)</Label>
        <Input id="keyword" type="text" placeholder="Welcome"
          value={keyword} onChange={(e) => setKeyword(e.target.value)} />
      </div>
      <label className="flex items-center gap-2">
        <Checkbox checked={screenshot} onCheckedChange={(v) => setScreenshot(v === true)} />
        <span className="text-sm">Take screenshot</span>
      </label>
    </>
  );
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkbrowser/config.go` | New |
| `server/internal/checkers/checkbrowser/checker.go` | New |
| `server/internal/checkers/checkbrowser/errors.go` | New |
| `server/internal/checkers/checkbrowser/samples.go` | New |
| `server/internal/checkers/checkbrowser/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/chromedp/chromedp`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Chrome/Chromium detection works (clear error if not installed)
- [ ] Create a browser check via the UI
- [ ] Verify page load metrics are captured
- [ ] Verify keyword detection works
- [ ] Verify wait_selector works
- [ ] Verify screenshot is captured and accessible
- [ ] Verify SPA pages render correctly
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
