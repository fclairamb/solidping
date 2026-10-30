---
model: sonnet
effort: medium
---

# Server errors reach Sentry only, never PostHog error tracking

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/36

## Source

When PostHog is active, its error-tracking view only ever holds `$exception` events
from the dashboard (`$lib=web`): over two weeks on a production install, none came
from the Go server. Server faults (5xx / `INTERNAL_ERROR` responses, recovered
panics) go to Sentry only, so whoever reviews PostHog replays together with error
tracking cannot see the server error behind a broken flow in a replay.

Decision recorded on the issue (final): the server reports its errors to PostHog
error tracking **too**. Send them as `$exception` events whenever PostHog is active
(5xx / `INTERNAL_ERROR` responses, recovered panics in the HTTP, check-worker and
job-worker paths), with the request's distinct id and session id when the dashboard
sent them. Sentry stays as it is when a DSN is configured: the two sinks are
independent, so a self-hosted install can use either one, both, or none.

## Problem

- `server/internal/analytics/analytics.go`: the PostHog client only enqueues
  `posthog.Capture` product events (`Capture`, around line 98). Its `Client`
  interface has no way to send an exception, although posthog-go v1.39.0 (already
  in `server/go.mod`) supports `posthog.Exception` / `posthog.NewDefaultException`
  (`error_tracking.go`).
- Every server error report goes through Sentry only:
  - `server/internal/errorreport/errorreport.go:24` `CaptureOnRequest` (called by
    `server/internal/handlers/base/base.go:281` `WriteErrorErr` for status >= 500
    and `base.go:295` `WriteInternalError`);
  - `errorreport.go:41` `CaptureWithTags` (called by the check-worker panic
    recovery, `server/internal/checkworker/worker.go:1642`, and the job-worker one,
    `server/internal/jobs/jobworker/worker.go:477`);
  - `server/internal/middleware/recover.go:70` calls `hub.RecoverWithContext`
    directly, bypassing `errorreport`.
- The dashboard does not tell the server which PostHog session or distinct id a
  request belongs to: `web/dash0/src/api/client.ts` (`doFetch`, ~line 369) sets only
  `Authorization` / `Content-Type`, and `web/dash0/src/lib/analytics.ts` does not
  enable posthog-js tracing headers. Without them a server exception cannot be
  linked to its replay.

## Why a spec

- Cross-cutting: one new sink wired into the HTTP path, the recover middleware,
  two background workers, and a dashboard change to propagate the session id.
- Over ~4 source files and ~150 lines with tests.
- Privacy: the redaction rules Sentry applies (`server/internal/app/sentry_scrub.go`)
  must hold for the new sink too.

## Proposal

1. **Analytics client: exceptions.** Extend `analytics.Client` with
   `CaptureException(ctx context.Context, exc Exception)` where `Exception` carries:
   the `error`, an optional stack (string), `OrgUID`, `UserUID`, an optional
   `SessionID`, an optional `DistinctID` override, and low-cardinality `Tags`
   (`map[string]string`, empty values skipped, same as `CaptureWithTags`). The no-op
   client swallows it; the PostHog client enqueues a `posthog.Exception`
   (`NewDefaultException` or a hand-built `ExceptionList` with type = Go error type
   name, value = redacted message), `$session_id` when known, the tags as
   properties, and `$lib` left to the SDK (`posthog-go`), so the PostHog view can
   tell server from web errors. Delivery stays best-effort and non-blocking; never
   return an error to the caller. Add a package-level `CaptureException` helper next
   to `Capture`.
2. **Redaction.** Before enqueueing, run the message and stack through the same
   rules as Sentry: no request body, no `Authorization` / `Cookie`, and the
   credential query params of `sentryCredentialParams` redacted from any URL in the
   message. Move the shared parts of `sentry_scrub.go` (the param set and the URL
   redactor) to a small package both sinks import, rather than duplicating them. Do
   not attach the request URL's query string, headers or body to the event: method
   and route pattern (`chi` route pattern, not the raw path) are enough.
3. **One funnel for both sinks.** In `errorreport`, make `CaptureOnRequest` and
   `CaptureWithTags` report to Sentry as today **and** to `analytics.CaptureException`.
   Make `middleware/recover.go` go through `errorreport` too (a new
   `RecoverOnRequest(r, panicValue, stack)` or equivalent), so the HTTP panic path is
   covered. Keep the two sinks independent: no DSN means Sentry is a no-op, PostHog
   off means the analytics client is a no-op; neither depends on the other.
   - HTTP paths: the distinct id comes from the authenticated claims on the context
     (org UID + user UID, through `analytics.DistinctID`), else from the
     `X-POSTHOG-DISTINCT-ID` header, else `anonymous`; the session id from the
     `X-POSTHOG-SESSION-ID` header. Validate both headers (length cap, charset) and
     ignore malformed values.
   - Worker paths: distinct id `org:<uid>` from the `organization` tag when present,
     else a fixed `server` id; no session id.
4. **Dashboard: send the session headers.** In `web/dash0/src/api/client.ts`, when
   analytics is initialized, add `X-POSTHOG-SESSION-ID` (`posthog.get_session_id()`)
   and `X-POSTHOG-DISTINCT-ID` (`posthog.get_distinct_id()`) to API requests, through
   a small getter exported by `src/lib/analytics.ts` (no import of posthog-js in the
   API client, which must keep working when analytics is off). Same-origin requests
   only; never send them to third-party hosts. Check the CORS allowlist in
   `server/internal/app/server.go` if the API can be cross-origin.
5. **Tests.**
   - `analytics`: the PostHog client turns `CaptureException` into an `Exception`
     message with the expected distinct id, `$session_id`, tags, and a redacted
     message (use a fake `posthog.Client` / a recording transport); the no-op client
     sends nothing.
   - `errorreport`: one call reports to both a test Sentry hub
     (`errorreporttest.NewHub`) and a recording analytics client; with PostHog off
     only Sentry receives it; with no hub only PostHog does.
   - `handlers/base`: a 500 via `WriteInternalError` and a 503 via `WriteErrorErr`
     produce a PostHog exception carrying the session id from the header; a 4xx does
     not.
   - `middleware/recover`: a panicking handler produces one PostHog exception.
   - Worker panics (check worker, job worker): one exception each, with the tags.
   - Vitest for the header getter (absent when analytics is off).
6. **Docs.** Update the analytics package doc comment ("product events only" no
   longer holds) and the PostHog section of the self-hosting / config docs: what the
   server sends to error tracking, what it never sends, and that Sentry and PostHog
   are independent.

## Closing the issue

This spec closes fclairamb/solidping-business#36 (tracked in another repo). The
implementing PR body **must** carry the line `Closes fclairamb/solidping-business#36`,
without quoting the issue text (solidping is public). When the spec is archived to
`specs/done/`, verify the issue is closed and, if the merge did not close it, close it
by hand:

    gh issue close 36 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
