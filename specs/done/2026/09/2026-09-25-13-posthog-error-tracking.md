---
model: sonnet
effort: medium
---

# Dashboard errors never reach PostHog error tracking

## Problem

On 2026-09-25 replays showed sessions with 1 to 4 console errors each, plus
nightly sessions with 20 to 80. PostHog error tracking had nothing: the
Solidping project has **never** received a `$exception` event
(`select count() from events where event = '$exception'` → 0).

Three separate gaps:

1. **Exception autocapture is off.** `posthog.init` in
   [analytics.ts](web/dash0/src/lib/analytics.ts) does not set
   `capture_exceptions`. The project setting `autocapture_exceptions_opt_in` is
   unset too. Replay showed the errors only because console-log recording
   (`capture_console_log_opt_in`) is on.
2. **Most of our errors are `console.error` calls, not throws.** For example
   `[auth] token refresh failed: …` in
   [token-refresh.ts](web/dash0/src/lib/token-refresh.ts). Plain exception
   autocapture (`window.onerror` / `unhandledrejection`) never sees them.
3. **React boundaries swallow crashes.** `ErrorBoundary.componentDidCatch` and
   `RouteErrorFallback` in
   [error-boundary.tsx](web/dash0/src/components/shared/error-boundary.tsx)
   only `console.error`. A page crash rendered as the fallback card is invisible
   to autocapture.

The nightly noise comes from something else: email link scanners
(`Uncaught (in promise) Object Not Found Matching Id:N, MethodName:update, ParamCount:4`),
which open `forgot-password` and login links around 00:45 to 03:00 UTC. That
signature is a scanner-side bug (CefSharp-based Microsoft Safe Links-style
crawlers), not ours. Once capture is on, it would drown real errors.

## Proposal

1. In `initAnalytics`, set:
   ```ts
   capture_exceptions: {
     capture_unhandled_errors: true,
     capture_unhandled_rejections: true,
     capture_console_errors: true,
   },
   ```
   Code config is the source of truth. Do not rely on the project toggle, so
   a self-hosted install with its own PostHog project behaves the same.
2. Add `captureException(error, properties?)` to `analytics.ts`, a no-op when
   analytics is off (same rule as `captureEvent`), and extend the local
   `PostHogLike` type. Call it from `componentDidCatch` (with
   `componentStack`) and from `RouteErrorFallback`'s effect (with the route
   id). Keep the existing `console.error` for the bug-report ring buffer, and
   make sure the error is not reported twice once `capture_console_errors` is
   on. Either skip the console capture for those two messages or drop the
   explicit call; pick one and test it.
3. Filter known third-party noise in `before_send` (compose with the redaction
   hook from spec `2026-09-25-11` if it has landed): drop `$exception` events
   whose message matches `/^Object Not Found Matching Id:\d+, MethodName:\w+, ParamCount:\d+$/`.
   Keep the list in one exported constant with a comment on each entry saying
   where it comes from.
4. Update `web/docs/…/analytics.md` ("Exactly what is sent") to say that error
   events are sent when analytics is enabled, and that they are unmasked like
   the rest.

## Tests

- `analytics.test.ts`: init options include the three `capture_exceptions`
  flags; `captureException` is a no-op before init and forwards after init.
- `before_send` filter: the scanner message is dropped; a normal
  `TypeError: x is undefined` passes (positive control).
- Boundary: rendering a component that throws under `ErrorBoundary` calls
  `captureException` exactly once (no double report through console capture).
- Manual on dev: throw from the browser console
  (`setTimeout(() => { throw new Error("sp-test") })`) and see it in PostHog
  error tracking within a minute.
