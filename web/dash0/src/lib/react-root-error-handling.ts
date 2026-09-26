/**
 * `createRoot` error-handling options (spec 2026-09-25-13), factored out of
 * main.tsx so its regression test (error-boundary.exception-dedupe.test.tsx)
 * exercises the EXACT function main.tsx installs, rather than a
 * reimplementation that could silently drift from it.
 */

/**
 * With no `onCaughtError`, React's own `defaultOnCaughtError` calls
 * `console.error(error)` for EVERY error a class boundary catches
 * (`ErrorBoundary`, and TanStack Router's `CatchBoundaryImpl` behind
 * `RouteErrorFallback`) — and it runs BEFORE `componentDidCatch`. With
 * `capture_console_errors` on, posthog-js autocaptures that `console.error`
 * with no signature registered yet (analytics.ts's
 * `dedupeAutocapturedBoundaryExceptions` has nothing to match it against), so
 * it ships as an extra, context-free `$exception` alongside the explicit
 * `captureException()` call `componentDidCatch` / `RouteErrorFallback` make
 * right after. Silencing `onCaughtError` removes that race entirely —
 * `ErrorBoundary` and `RouteErrorFallback` already log to `console.error`
 * themselves (the bug-report ring buffer) and report to PostHog explicitly,
 * with real `componentStack` / `routeId` context.
 *
 * `onUncaughtError` / `onRecoverableError` are deliberately left at their
 * React defaults (not exported here, not overridden in main.tsx): an error
 * with NO boundary to catch it never reaches `componentDidCatch` at all, so
 * there is no explicit `captureException` call for it to race against.
 * React's default there (`reportGlobalError`, i.e. the browser's native
 * `reportError()`) surfaces it as a real `window.onerror`, which
 * posthog-js's `capture_unhandled_errors` already autocaptures exactly once.
 */
export function onCaughtError(): void {
  // Intentionally empty — see the module comment above.
}

/** The options object main.tsx passes to `createRoot`. */
export const reactRootErrorOptions = { onCaughtError };
