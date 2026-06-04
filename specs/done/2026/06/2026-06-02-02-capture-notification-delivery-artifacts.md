# Capture notification delivery artifacts

## Problem

When a notification fails — the common case being
`webhook request failed: status 42…` in the incident Notifications table — the
data that would actually let an operator debug it is thrown away.

In `server/internal/notifications/webhook.go` (`Send`, ≈ lines 165–203) the
sender already knows, at send time:

- the target URL,
- the exact signed JSON payload it sent (`body`),
- the HTTP status code (`resp.StatusCode`),
- the response body (`respBody`).

On a non-2xx it collapses all of that into a single error string —
`fmt.Errorf("%w: status %d: %s", ErrWebhookRequestFailed, resp.StatusCode, string(respBody))`
(line 202) — which then lands, **truncated and unstructured**, in
`incident_notifications.error`. The structured status code, the request payload,
and the response body are discarded.

So the detail page from `2026-06-02-01-notification-detail-route.md` can show
*that* a webhook returned a 4xx, but not the response body that explains why, not
the payload that was sent, and you can't filter deliveries by status code.

We already model this better elsewhere: the integration **test-send** path carries
a structured `StatusCode` on its result
(`server/internal/handlers/integrations/service.go`, ≈ lines 845–851 / 1021–1026).
The real delivery path just doesn't persist the equivalent.

## Goal

Persist structured delivery artifacts per notification attempt and surface them on
the detail page, so a failed webhook is debuggable without re-running it.

## What to capture

Per attempt, where the channel makes it available:

- `httpStatusCode` (int)
- `requestUrl` (host + path, **with query string and secrets stripped**) or just
  the host — never the raw URL if it can carry credentials
- `requestBody` — the payload sent, capped (e.g. 16 KB)
- `responseBody` — capped (e.g. 16 KB)
- `durationMs`
- optionally a small **allowlisted** set of response headers (e.g. `Retry-After`,
  `Content-Type`)

Webhook is the priority (it's the channel in the screenshot). Email/Slack/Discord
fill in what they can (e.g. provider message id is already captured as
`messageId`); a missing field is simply omitted.

## Schema

Add one nullable `delivery_details` column to `incident_notifications`:

- Postgres: `jsonb`
- SQLite: `text` holding JSON (keep parity per the sync-pg-to-sqlite workflow)

A single JSON blob avoids per-channel column churn and keeps webhook/email/Slack
differences flexible. Migration `NNN_incident_notification_delivery_details.(up|down).sql`
in both `server/internal/db/postgres/migrations` and
`server/internal/db/sqlite/migrations`, plus the field on
`server/internal/db/models/incident_notification.go`.

*Alternative considered:* discrete `http_status_code` + `request_body` +
`response_body` columns. Rejected — only the status code is worth
indexing/filtering, and it can be promoted to its own column later; the rest is
channel-shaped and belongs in JSON.

## Plumbing

- Senders return **structured** delivery info, not just an error string. Smallest
  change: a typed `DeliveryResult` (status code, response body, duration, captured
  request) that senders populate on both success and failure. `webhook.go` `Send`
  fills it; other senders fill what they can.
- The escalation-step job writes the captured detail onto the model when it
  records the row — see the `CreateIncidentNotification` calls in
  `server/internal/jobs/jobtypes/job_escalation_step.go` (≈ lines 330, 503, 567,
  668, 695).
- Cap/truncate bodies **before** persistence; redact secret headers.

## Frontend

- Extend the detail DTO/type (`IncidentNotification` / `NotificationDetail`) with
  `deliveryDetails`.
- On the detail page (from spec 01) add a **Delivery** section: status-code badge,
  duration, request payload (collapsible, monospace, copyable), response body
  (collapsible, copyable).
- Hide the section entirely when no details were captured (older rows, or channels
  that don't produce them).

## Privacy / safety

- **Never** persist the webhook signing secret, `Authorization`, or custom secret
  headers. Strip secrets from the stored URL.
- Cap body sizes. Response bodies may contain data from the receiver — they are
  org-scoped and gated behind the same auth as the rest of the incident; document
  this.

## Scope

- Backend: migration (Postgres + SQLite), model field, db read/write, sender
  `DeliveryResult` type, `webhook.go` capture, escalation-step job wiring, detail
  DTO.
- Frontend: type + the Delivery section on the detail page.
- Tests: unit test that the webhook sender captures status/body on failure and
  redacts secrets; e2e that a failed webhook shows the Delivery section.

## Non-goals

- Retry/replay of notifications.
- Backfilling artifacts for already-stored historical rows — new attempts only.

## Acceptance criteria

1. A failed webhook stores `httpStatusCode` + response body (capped) + request
   payload in `delivery_details`.
2. The detail page renders them; request/response bodies are copyable.
3. Signing secrets, auth headers, and URL credentials never appear in stored
   details (asserted by test).
4. SQLite and Postgres behave identically.
5. Rows without details (pre-migration / unsupported channel) render the page
   without the Delivery section — no crash, no empty box.

## Implementation Plan

### Backend
1. **Migration 036** — `036_incident_notification_delivery_details.(up|down).sql`
   in both `postgres/migrations` and `sqlite/migrations`. Adds one nullable
   `delivery_details` column: Postgres `jsonb`, SQLite `text`. Down drops it.
2. **Model field** — add `DeliveryDetails *models.DeliveryDetails` to
   `IncidentNotification` (`delivery_details,type:jsonb,nullzero`). Define a typed
   `DeliveryDetails` struct in `models` with `Value`/`Scan` (driver.Valuer /
   sql.Scanner) so it persists as JSON on both engines. All fields `omitempty`.
3. **Sender DeliveryResult** — carry structured delivery info back via a
   `DeliveryDetails *models.DeliveryDetails` field on `notifications.Payload`
   (same side-channel pattern as the existing `MessageID`), avoiding a breaking
   change to the `Sender` interface across all 11 senders.
4. **webhook.go capture + redaction** — in `Send`, measure duration, capture
   `httpStatusCode`, `requestUrl` (host+path only — query string and userinfo
   stripped via a `redactURL` helper), `requestBody` (capped 16 KB), and
   `responseBody` (capped 16 KB, read on every response not just non-2xx).
   Allowlist a small set of response headers (`Retry-After`, `Content-Type`).
   Never store the signing secret, `Authorization`, or custom headers.
5. **DB read/write parity** — extend `MarkIncidentNotificationSentByJob` and
   `MarkIncidentNotificationFailedByJob` (postgres + sqlite + `db.Service`
   interface) to also set `delivery_details`. The notification job
   (`sendAndAudit`) passes `payload.DeliveryDetails` through. `GetIncidentNotification`
   already does `n.*` so the column is read automatically.
6. **DTO** — add `DeliveryDetails *models.DeliveryDetails` (JSON key
   `deliveryDetails,omitempty`) to `NotificationDetail` and map it in
   `toNotificationDetail`.

### Frontend
7. **Type** — add `deliveryDetails?: DeliveryDetails` to the `IncidentNotification`
   interface in `hooks.ts`.
8. **Delivery section** — on the detail page add a Delivery card rendered only
   when `deliveryDetails` is present: status-code badge, durationMs, requestUrl,
   request payload (collapsible `<details>`, monospace, copyable), response body
   (collapsible, copyable), and any allowlisted response headers. Hidden entirely
   when absent. Add a Collapsible entry to the design reference.

### Tests
9. **Unit** — webhook sender test: a failing webhook (non-2xx) populates
   `DeliveryDetails` with status code + capped response body + request payload,
   and a test asserting the signing secret / Authorization header / URL
   credentials never appear in the stored details.
10. **e2e** — extend `notification-detail.spec.ts`: a failed webhook surfaces the
    Delivery section with a status-code badge and copyable bodies.
