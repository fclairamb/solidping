---
model: sonnet
effort: low
---

# POST /api/mgmt/report is anonymous and unthrottled — free storage fill and issue spam

## Problem

`POST /api/mgmt/report` (`handlers/feedback/handler.go:44-89`,
`service.go:93-161`) accepts screenshots and issue text with deliberately
permissive auth and persists them through `files.Service.CreateFile` (10 MB
cap) into the first org's storage when no org is given
(`service.go:100-113`), then dispatches a GitHub issue when
`App.EnableBugReport` is on (`service.go:155-158`).

The rate limiter only scopes to `/api/v1/` (`middleware/ratelimit.go:24-29`),
so this route has **no rate limit and no concurrency cap**: repeated
multipart posts fill local/S3 storage and the feedback `files` table
indefinitely and spam issues at the wired repo token.

## Proposal

1. Give the route a derived limiter, mirroring the existing
   `deviceConsentRateLimitConfig` pattern (`app/server.go:734-735`): e.g.
   **5 requests/min per IP**, burst 10 — report submission is a human action.
2. Small per-org storage quota for feedback files: cap total stored
   feedback-file bytes per org (system parameter, e.g. 100 MB default);
   when exceeded, return 413 with a clear message and log a WARN.
3. When `App.EnableBugReport` is false (no issue dispatch): still accept and
   store the report (it is the in-app feedback channel) — the limiter and
   quota are what protect storage; issue dispatch additionally gets a
   per-hour cap (e.g. 20/h, in-process counter) so a burst cannot spam the
   repo token.
4. Docs/changelog: no API change.

## Tests

- Handler test: 6 posts in a minute from one IP → 6th is 429 with the
  standard error shape; limiter resets after the window.
- Storage quota: seed feedback files over the cap → next report 413, no row
  written (both DBs).
- Issue dispatch: 21st dispatch within an hour is skipped with a WARN log
  (fake GitHub client).
- Route-level: the limiter applies even when the request has no org and no
  auth (the anonymous case is the point of the fix).