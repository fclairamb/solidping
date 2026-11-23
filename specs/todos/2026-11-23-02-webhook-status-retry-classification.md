---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Webhook deliveries ignore the HTTP status: 4xx log an unexpected ERROR, 5xx/429/408 are never retried

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/106

## Source

Production logs showed a burst of `Job failed` lines at ERROR (`job_type` `notification` and
`webhook`, `permanent: false`, `retryable: false`, `retry_count: 0`), all against one customer
webhook endpoint answering `HTTP 405`. The burst followed incidents opened by a server restart.
The reporter is right on both counts below; the references to the customer are not to be
reproduced anywhere (use `acme` placeholders).

## Problem

Both webhook senders turn any non-2xx status into a plain error, so the job worker cannot tell
a client error from a server error:

- `server/internal/jobs/jobtypes/job_webhook.go:155-156`: `fmt.Errorf("%w: HTTP %d: %s",
  ErrWebhookHTTPError, …)` for every status `>= 300`. Only transport errors are wrapped
  retryable (line 129).
- `server/internal/notifications/webhook.go:276-277`: `fmt.Errorf("%w: status %d: %s",
  ErrWebhookRequestFailed, …)`. `job_notification.go:384` (`sendAndAudit`) retries only
  `notifications.IsNetworkError(err) || jobdef.IsRetryable(err)`.
- `server/internal/jobs/jobworker/worker.go:530-534` then logs neither-permanent-nor-retryable
  failures at ERROR (`jobFailureLog`, line 549), and does not retry them.

Consequences:

1. **4xx** (misconfigured endpoint: 404, 405, 410…): an ERROR for what is user input; the
   endpoint fails the same way on every incident.
2. **5xx / 429 / 408**: the alert is dropped after one attempt, although the receiver may
   recover within the 1/5/15 min backoff (`jobsvc/service.go:999`). This loses notifications.
3. The status code is not on the failure log line; it is only in the job output.

Related facts found while diagnosing:

- `notifications/webhook.go:182` draws a fresh `webhook-id` (`uuid.New()`) on every send, so a
  retry carries a new id and a Standard Webhooks receiver cannot deduplicate it.
- `handlers/integrations/service.go:2231` (`statusCodeFromErr`) parses the status back out of
  the error string; a typed error would replace that parsing.
- Retry scheduling (`jobsvc/service.go:992` `createRetryClone`) has a fixed backoff and no way
  to honour a `Retry-After` hint.

## Why a spec

- Touches the job worker, the job service retry scheduling, the jobdef error types and two
  senders (more than ~4 source files once `Retry-After` and the log attribute are in).
- Retrying webhook deliveries raises a design question (idempotency of `webhook-id` across
  retries) that the report did not settle.

## Proposal

1. **Typed HTTP status error** (`server/internal/notifications/` or `jobdef`): e.g.
   `type HTTPStatusError struct { StatusCode int; RetryAfter time.Duration; Err error }` with
   `Unwrap`, plus a classifier `ClassifyHTTPStatus(code int, retryAfter time.Duration, err error) error`:
   - `408`, `429`, `>= 500` → `jobdef.NewRetryableError(statusErr)`;
   - other `4xx` (and `3xx`, since redirects are already followed by the client) →
     `jobdef.NewPermanentError(statusErr)`.
   Keep `errors.Is(err, ErrWebhookHTTPError)` / `ErrWebhookRequestFailed` true (wrap the
   sentinel inside).
2. **Senders**: `job_webhook.go:155` and `notifications/webhook.go:276` return the classified
   error. Parse `Retry-After` (seconds or HTTP date, clamped, reusing the approach of
   `integrations/discord/bot_client.go:222`) on 429/503.
3. **`sendAndAudit`** (`job_notification.go:384`): unchanged logic already passes retryable
   through; make sure a permanent error is returned as-is (it is) and the audit row records
   `retryable=false`.
4. **Worker log** (`jobworker/worker.go:533`): when `errors.As(err, &HTTPStatusError)` (or a
   small `interface{ HTTPStatusCode() int }` so the worker does not import notifications), add
   `status_code` to the `Job failed` line.
5. **Retry-After**: let the retry clone be scheduled at `max(backoff, RetryAfter)` capped at
   the last backoff step (15 min), via an optional hint carried by `RetryableError` (e.g.
   `RetryableError.After time.Duration`) read in `handleRetry` → `RetryJob`.
6. **Replace `statusCodeFromErr`** string parsing in `handlers/integrations/service.go:2231`
   with `errors.As` on the typed error.
7. **`webhook-id` across retries**: see open question 1.

## Tests

- `notifications/webhook_test.go`: httptest receiver answering 405 → `jobdef.IsPermanent`;
  503 and 429 → `jobdef.IsRetryable`; 429 with `Retry-After: 30` → hint 30 s; 200 → nil.
- `jobs/jobtypes/job_webhook_test.go`: same matrix for the `webhook` job.
- `jobs/jobtypes/job_notification_run_test.go`: a 503 leaves the audit row pending/retryable
  and returns a retryable error; a 404 marks it failed, non-retryable, permanent.
- `jobs/jobworker`: `jobFailureLog` / failure line carries `status_code` for a typed error
  (and no `status_code` for a non-HTTP error).
- `jobs/jobsvc`: a retryable error with `After` larger than the backoff schedules the clone
  at that delay, capped.
- `handlers/integrations`: the integration test endpoint still reports the status code.

## Open questions

1. **Should a retried notification webhook keep the same `webhook-id`?** Recommended: yes,
   derive it from the job chain (the original job UID, i.e. the first `previous_job_uid`) so
   Standard Webhooks receivers can deduplicate; trade-off: a little plumbing to find the root
   job, versus receivers possibly processing the same alert twice after a 5xx that actually
   committed.
2. **Surface repeated permanent failures to the org (integration health)?** Recommended: out
   of scope here, file separately if wanted; this spec only fixes classification, retries and
   logging.

## Resolved open questions

1. A retried notification webhook keeps the same `webhook-id`: derive it from the job chain (the root job UID, the first `previous_job_uid`) so receivers can deduplicate. (autopilot: recommended answer)
2. Surfacing repeated permanent failures to the org (integration health) is out of scope: this spec only fixes classification, retries and logging. (autopilot: recommended answer)

## Closing the issue

This spec closes fclairamb/solidping-business#106. The implementing PR body **must** carry
`Closes fclairamb/solidping-business#106` so the squash-merge closes it. When the spec is
archived to `specs/done/`, verify the issue is closed and, if the merge did not close it,
close it by hand:

    gh issue close 106 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
