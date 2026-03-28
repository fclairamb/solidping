# Sentry Integration

## Overview

SolidPing needs centralized error tracking and performance monitoring beyond what structured logging and OpenTelemetry provide. Sentry excels at error grouping, stack trace enrichment, release tracking, and alerting — things that are painful to build on raw OTel alone.

**Use cases:**
- Detect and group recurring errors in production (both backend and frontend)
- Track error rates per release to catch regressions early
- Capture frontend errors that never reach the backend (JS exceptions, chunk load failures, React render crashes)
- Get actionable alerts with full context (user, organization, check type) instead of grepping logs
- Monitor performance (slow API endpoints, slow page loads) with automatic transaction tracing

**Design decisions:**
- **Two DSNs**: One for the Go backend (`solidping-server`), one for the React frontend (`solidping-dash`). Rationale: separate rate limits, separate alert rules, separate release tracking, separate environments. A single DSN would mix Go panics with JS TypeError noise, making triage harder.
- **Environment-based filtering**: `development`, `staging`, `production` — set via config, not hardcoded
- **OpenTelemetry coexistence**: Sentry runs alongside OTel, not replacing it. OTel handles metrics/traces/logs to a collector; Sentry handles error grouping, alerting, and release health. Sentry's OTel integration can link traces if needed later.
- **Sensitive data scrubbing**: Strip tokens, passwords, and PII before sending to Sentry (server-side + SDK-side)
- **Sampling**: 100% error capture, configurable transaction sampling (default 10% in production)

---

## Backend Integration

### Dependencies

**File**: `server/go.mod`

```
github.com/getsentry/sentry-go v0.31.1
```

No need for `sentry-go/http` — we'll use the core SDK with a custom middleware for bunrouter.

### Configuration

**File**: `server/internal/config/config.go`

```go
type SentryConfig struct {
    DSN              string  `koanf:"dsn"`                // Sentry DSN (empty = disabled)
    Environment      string  `koanf:"environment"`        // development, staging, production
    TracesSampleRate float64 `koanf:"traces_sample_rate"` // 0.0 to 1.0 (default 0.1)
    Debug            bool    `koanf:"debug"`              // Enable Sentry debug logging
}
```

**Config examples:**

```yaml
# config.yml
sentry:
  dsn: "https://examplePublicKey@o0.ingest.sentry.io/0"
  environment: "production"
  traces_sample_rate: 0.1
```

```bash
# Environment variables (override config file)
SP_SENTRY_DSN=https://...@sentry.io/...
SP_SENTRY_ENVIRONMENT=production
SP_SENTRY_TRACES_SAMPLE_RATE=0.1
```

### Initialization

**File**: `server/internal/app/app.go` (in the application setup)

```go
func initSentry(cfg config.SentryConfig, version string) error {
    if cfg.DSN == "" {
        slog.Info("Sentry disabled (no DSN configured)")
        return nil
    }

    err := sentry.Init(sentry.ClientOptions{
        Dsn:              cfg.DSN,
        Environment:      cfg.Environment,
        Release:          "solidping-server@" + version,
        TracesSampleRate: cfg.TracesSampleRate,
        Debug:            cfg.Debug,
        BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
            // Scrub sensitive headers
            for i := range event.Request.Headers {
                if event.Request.Headers[i].Key == "Authorization" {
                    event.Request.Headers[i].Value = "[FILTERED]"
                }
                if event.Request.Headers[i].Key == "Cookie" {
                    event.Request.Headers[i].Value = "[FILTERED]"
                }
            }
            return event
        },
    })
    if err != nil {
        return fmt.Errorf("sentry init: %w", err)
    }

    slog.Info("Sentry initialized", "environment", cfg.Environment)
    return nil
}
```

**Shutdown**: Call `sentry.Flush(2 * time.Second)` in the application's graceful shutdown sequence.

### Middleware

**File**: `server/internal/middleware/sentry.go`

Custom middleware for bunrouter that:
1. Creates a Sentry hub clone per request
2. Sets request context (method, URL, headers)
3. Enriches scope with user/org context from auth claims
4. Recovers panics and reports them to Sentry
5. Reports 5xx responses as errors

```go
func SentryMiddleware() bunrouter.MiddlewareFunc {
    return func(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
        return func(w http.ResponseWriter, req bunrouter.Request) error {
            hub := sentry.GetHubFromContext(req.Context())
            if hub == nil {
                hub = sentry.CurrentHub().Clone()
            }

            hub.Scope().SetRequest(req.Request)

            // Add user context if authenticated
            if claims, ok := GetClaims(req.Context()); ok {
                hub.Scope().SetUser(sentry.User{
                    ID:    claims.UserUID,
                    Email: claims.Email,
                })
                hub.Scope().SetTag("organization", claims.OrgSlug)
            }

            ctx := sentry.SetHubOnContext(req.Context(), hub)
            req = req.WithContext(ctx)

            defer func() {
                if r := recover(); r != nil {
                    hub.RecoverWithContext(ctx, r)
                    // Re-panic so the existing recovery middleware can handle the HTTP response
                    panic(r)
                }
            }()

            return next(w, req)
        }
    }
}
```

### Error Reporting in Handlers

**File**: `server/internal/handlers/base/base.go`

Modify `WriteErrorErr()` and `WriteInternalError()` to capture errors in Sentry:

```go
func (h *HandlerBase) WriteInternalError(w http.ResponseWriter, r *http.Request, err error) {
    // Existing error response logic...

    // Report to Sentry
    if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
        hub.CaptureException(err)
    }
}
```

Only 5xx errors get reported to Sentry. 4xx errors are expected behavior (validation, auth) and should not generate Sentry events.

### Check Execution Errors

**File**: `server/internal/scheduler/` (or wherever check execution happens)

When a check execution itself fails (not the target being down, but the checker crashing), report to Sentry with context:

```go
sentry.WithScope(func(scope *sentry.Scope) {
    scope.SetTag("check.type", check.Type)
    scope.SetTag("check.uid", check.UID)
    scope.SetTag("organization", org.Slug)
    scope.SetContext("check", map[string]interface{}{
        "name":   check.Name,
        "target": check.Target,
        "region": worker.Region,
    })
    sentry.CaptureException(err)
})
```

**Important distinction**: A check returning "target is down" is normal operation — do NOT report that to Sentry. Only report unexpected errors (driver crashes, panics, configuration errors).

---

## Frontend Integration

### Dependencies

**File**: `web/dash0/package.json`

```json
{
  "dependencies": {
    "@sentry/react": "^9.x"
  }
}
```

The `@sentry/react` package includes browser tracing, React error boundary integration, and React Router support.

### Configuration

**File**: `web/dash0/src/lib/sentry.ts`

```typescript
import * as Sentry from "@sentry/react";

export function initSentry() {
  const dsn = import.meta.env.VITE_SENTRY_DSN;
  if (!dsn) return;

  Sentry.init({
    dsn,
    environment: import.meta.env.VITE_SENTRY_ENVIRONMENT ?? "development",
    release: `solidping-dash@${import.meta.env.VITE_APP_VERSION ?? "dev"}`,
    tracesSampleRate: parseFloat(import.meta.env.VITE_SENTRY_TRACES_SAMPLE_RATE ?? "0.1"),

    integrations: [
      Sentry.browserTracingIntegration(),
      Sentry.replayIntegration({
        maskAllText: true,
        blockAllMedia: true,
      }),
    ],

    // Session replay: capture 0% of sessions, 100% of sessions with errors
    replaysSessionSampleRate: 0,
    replaysOnErrorSampleRate: 1.0,

    // Scrub sensitive data
    beforeSend(event) {
      // Remove auth tokens from breadcrumbs
      if (event.breadcrumbs) {
        event.breadcrumbs = event.breadcrumbs.map((bc) => {
          if (bc.category === "xhr" || bc.category === "fetch") {
            if (bc.data?.url) {
              // Strip query params that might contain tokens
              try {
                const url = new URL(bc.data.url, window.location.origin);
                url.searchParams.delete("token");
                bc.data.url = url.toString();
              } catch {
                // Ignore malformed URLs
              }
            }
          }
          return bc;
        });
      }
      return event;
    },

    // Don't send errors from development
    enabled: import.meta.env.PROD,
  });
}
```

### Error Boundary Integration

**File**: `web/dash0/src/components/shared/error-boundary.tsx`

Wrap the existing error boundary with Sentry's error boundary:

```tsx
import * as Sentry from "@sentry/react";

// Replace the existing ErrorBoundary class with Sentry's
export function AppErrorBoundary({ children }: { children: React.ReactNode }) {
  return (
    <Sentry.ErrorBoundary
      fallback={({ error, resetError }) => (
        <ErrorFallback error={error} onReset={resetError} />
      )}
    >
      {children}
    </Sentry.ErrorBoundary>
  );
}
```

### User Context

**File**: `web/dash0/src/main.tsx` (or wherever auth state is managed)

After successful login / token refresh, set Sentry user context:

```typescript
Sentry.setUser({
  id: user.uid,
  email: user.email,
});
Sentry.setTag("organization", currentOrg.slug);
```

On logout:

```typescript
Sentry.setUser(null);
```

### API Error Reporting

**File**: `web/dash0/src/api/client.ts`

Report API errors (5xx only) to Sentry from the API client:

```typescript
if (response.status >= 500) {
  Sentry.captureException(new ApiError(/*...*/), {
    tags: {
      "api.endpoint": url,
      "api.method": method,
      "api.status": response.status,
    },
  });
}
```

Do NOT report 4xx errors to Sentry — they are handled by the UI.

### Environment Variables

**File**: `web/dash0/.env.example`

```bash
VITE_SENTRY_DSN=
VITE_SENTRY_ENVIRONMENT=development
VITE_SENTRY_TRACES_SAMPLE_RATE=0.1
VITE_APP_VERSION=dev
```

---

## Sentry Project Setup

### Projects

| Sentry Project | Platform | DSN Variable | Release Format |
|---|---|---|---|
| `solidping-server` | Go | `SP_SENTRY_DSN` | `solidping-server@{version}` |
| `solidping-dash` | React | `VITE_SENTRY_DSN` | `solidping-dash@{version}` |

### Alert Rules (suggested defaults)

| Alert | Condition | Action |
|---|---|---|
| New issue | First occurrence of a new error | Notify team channel |
| High frequency | >50 events in 1 hour | Page on-call |
| Regression | Previously resolved issue reappears | Notify team channel |
| Backend panic | Tag `level:fatal` | Page on-call |

### Source Maps

Upload frontend source maps during CI/CD build:

```bash
# In CI after building dash0
npx @sentry/cli releases files "solidping-dash@${VERSION}" upload-sourcemaps ./dist
```

The Go backend doesn't need source maps — Sentry's Go SDK captures full stack traces natively.

---

## Implementation Order

### Phase 1: Backend core (1-2 hours)
1. Add `sentry-go` dependency
2. Add `SentryConfig` to config
3. Initialize Sentry in app startup + flush on shutdown
4. Add Sentry middleware to bunrouter
5. Report 5xx errors from `WriteInternalError()`

### Phase 2: Frontend core (1-2 hours)
1. Add `@sentry/react` dependency
2. Create `sentry.ts` initialization
3. Call `initSentry()` in `main.tsx`
4. Replace error boundary with Sentry error boundary
5. Set user context on auth changes
6. Report 5xx API errors from client

### Phase 3: Enrichment (1 hour)
1. Add check execution error reporting with context tags
2. Add source map upload to CI/CD
3. Configure Sentry alert rules
4. Add Session Replay for error sessions

---

## What This Does NOT Cover

- **Replacing OpenTelemetry**: OTel stays for metrics, distributed traces, and log shipping. Sentry is for error tracking and alerting.
- **Worker-side Sentry**: Workers run check executions but share the same Go binary — they use the same backend DSN and initialization. No separate project needed.
- **Self-hosted Sentry**: This spec assumes Sentry SaaS (sentry.io). Self-hosted Sentry is a separate infrastructure decision.
- **Sentry Crons**: Could be used to monitor the scheduler heartbeat, but that's a future enhancement.
