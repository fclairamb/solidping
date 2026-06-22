# Status-Page Subscriber Notifications (email + RSS)

> Roadmap: **P1.2** (`wiki/roadmap.md`). No subscriber/subscription concept exists in the
> backend today.

## Context

Public status pages today are read-only: a visitor sees current status and the incident
timeline (`statuspages.ViewStatusPage`, served at `api.GET /status-pages/:org/:slug`,
`server.go:838`), but cannot ask to be told when something changes. Every major
competitor (UptimeRobot, Pingdom, Checkly, BetterStack, Atlassian Statuspage) lets end
users subscribe and get notified for the duration of an incident. This is the
single most-requested feature from public-status-page users.

This spec adds **email + RSS/Atom** subscriptions. SMS is explicitly deferred (Tier 3).

### What already exists (build on these, don't reinvent)

- **Status updates** are their own domain: `server/internal/handlers/statusupdates/`
  (`handler.go`, `service.go`), wired at `server.go:793-800`. `CreateStatusUpdate`
  (`POST /orgs/:org/status-updates`, `server.go:797`) is the **publish event** to hook
  for fan-out. Updates carry `Kind` (`investigating`/`identified`/`monitoring`/
  `resolved`/`maintenance`/`info`), `StatusPageUID`, optional `IncidentUID`, `Title`,
  `BodyMarkdown` — see `server/internal/db/models/status_update.go`.
- **Reusable email transport**: `server/internal/email` package — `email.Sender`
  interface, `email.Message{Recipients, Subject, HTML, Text}`. Injected as
  `services.Registry.EmailSender` (`server/internal/app/services/services.go:19`).
  **Use this directly.** Do **not** route through `server/internal/notifications/email.go`
  — that `EmailSender` is coupled to the incident `Payload`/`JobContext` and ack-link
  flow, which is the wrong shape for public subscriber mail.
- **Dual DB backends**: every table/query is implemented twice —
  `server/internal/db/sqlite/` and `server/internal/db/postgres/` — plus migrations in
  both `server/internal/db/{sqlite,postgres}/migrations/`. Latest migration is `030`;
  the new one is **`031`**. Models live in `server/internal/db/models/`. Keep both
  backends in lockstep (see the `sync-pg-to-sqlite` workflow).

## Goals

1. A visitor on a public status page (`web/status0`) can subscribe by email.
2. Subscriptions are **per status page** with optional **per-incident** scoping, so a
   subscriber isn't blanket-paged across unrelated incidents.
3. **Double opt-in**: a subscription is inactive until the email is confirmed.
4. Every notification carries a one-click unsubscribe link.
5. An **RSS/Atom feed** per status page exposes the same status-update timeline for users
   who prefer feeds over email.
6. Notifications fire on `CreateStatusUpdate` — reusing the existing status-update
   domain, **not** the internal incident alerting path.

## Non-goals

- SMS / Slack / webhook subscriber channels (competitor paid-tier features — defer).
- Letting subscribers pick which *kinds* of updates they get (all confirmed subscribers
  for the page/incident scope get every published update). Future refinement.
- Editing the incident-alerting notification fan-out.

## Data model

New model `server/internal/db/models/status_page_subscriber.go`, table
`status_page_subscriber`:

| Field | bun column | Notes |
|---|---|---|
| `UID` | `uid` pk varchar(36) | public id (`$uid` convention) |
| `OrganizationUID` | `organization_uid` notnull | tenant scope |
| `StatusPageUID` | `status_page_uid` notnull | FK to the page |
| `Email` | `email` notnull | PII — see security note |
| `ConfirmedAt` | `confirmed_at` | null until double opt-in completes |
| `ConfirmToken` | `confirm_token` notnull | opaque, single-purpose, unique |
| `UnsubscribeToken` | `unsubscribe_token` notnull | opaque, single-purpose, unique |
| `Scope` | `scope` notnull | `page` (all incidents) or `incident` |
| `IncidentUID` | `incident_uid` | set when `scope = incident` |
| `CreatedAt` | `created_at` notnull default now | |
| `DeletedAt` | `deleted_at` | soft delete on unsubscribe |

Migration `031_status_page_subscriber.{up,down}.sql` in **both** sqlite + postgres dirs.
Indexes: `(status_page_uid, confirmed_at)` for fan-out lookups; unique on `confirm_token`
and `unsubscribe_token`; partial-ish uniqueness on `(status_page_uid, email, scope,
incident_uid)` where `deleted_at IS NULL` to avoid duplicate live subscriptions (express
as a unique index over the columns incl. a `coalesce`'d incident_uid; sqlite/pg differ —
use a plain unique index and rely on the service to soft-undelete on re-subscribe).

## Backend

New domain package `server/internal/handlers/statussubscribers/` (`handler.go`,
`service.go`, `*_test.go`) — keeps the public/PII surface separate from the authed
status-page CRUD. DB methods (`CreateSubscriber`, `GetSubscriberByConfirmToken`,
`GetSubscriberByUnsubToken`, `ConfirmSubscriber`, `SoftDeleteSubscriber`,
`ListConfirmedSubscribers(statusPageUID, incidentUID *string)`, `ListSubscribers` for
admin) implemented in both `db/sqlite/status_page_subscriber.go` and
`db/postgres/status_page_subscriber.go`, plus interface entries in `db/service.go`.

### Subscriber mail templates

A new public-facing template set (separate from incident templates), 4 messages:
`confirm` (double opt-in), `incident-opened`, `update`, `resolved`. Build `email.Message`
HTML+Text and send via `services.EmailSender.Send(ctx, msg)`. Every non-confirm message
ends with a one-click unsubscribe link
(`{baseURL}/api/v1/public/status-subscribers/unsubscribe?token=…`). `baseURL` comes from
`config.Server.BaseURL`.

### Fan-out trigger

In `statusupdates.Service.CreateStatusUpdate` (after the update is persisted), enqueue
subscriber mail to the page's confirmed subscribers (scope `page`) plus the
`incident`-scoped subscribers matching `IncidentUID`. Map `Kind` →
template: `resolved` → resolved template; first update for an incident → incident-opened;
otherwise → update. **Respect group-incident correlation** so one outage produces one
subscriber mail, not one per member check (the status-update already anchors to a page +
optional incident, so this falls out naturally — one update = one mail wave).

Fan-out must not block the HTTP response or fail the update on a mail error: dispatch
through the existing async job/notifier path if one fits, else a fire-and-forget
goroutine that logs failures. Confirm which by checking how incident notifications are
enqueued (`server/internal/jobs/` / notifier) before implementing.

## API

Follow `CLAUDE.md` conventions (`data` envelope, `$uid` paths, camelCase, PATCH):

- `POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers` — start subscription
  (public, no auth; body `{ email, scope?, incidentUid? }`; returns **202**, sends
  confirm mail). **Rate-limited** (see security).
- `GET  /api/v1/public/status-subscribers/confirm?token=…` — confirm (sets
  `confirmed_at`); returns a small HTML page or redirects to the status page with a
  success banner.
- `GET  /api/v1/public/status-subscribers/unsubscribe?token=…` — soft-delete; returns a
  confirmation HTML page.
- `GET  /api/v1/status-pages/:org/:slug/feed.xml` — Atom feed of the status-update
  timeline; no auth; `Cache-Control` set; reuses `ListPublicStatusUpdates`
  (`statuspages/service.go:829`).
- `GET  /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers` (**authed**) — admin
  list: count + addresses for the dash0 edit route. Emails redactable.
- `DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers/:uid` (**authed**) —
  admin remove.

Register routes in `server.go` alongside the existing status-page block (~L805-838): the
two public routes outside `RequireAuth`, the two admin routes under it.

## UI

- **`web/status0`** public page: a "Subscribe to updates" widget (email field → "check
  your inbox" confirm state) plus a feed link (`feed.xml`). Mobile-usable per frontend
  conventions. Reuse design-reference primitives (form input, button, alert).
- **`web/dash0`** status-page edit route: a read-only subscriber list with count and a
  remove action (two ghost icon buttons / `Trash2` per row-action convention).

## Security / PII

- Subscriber emails are **PII**: never logged in clear, never exposed in *public* API
  responses, redacted in audit trails. The authed admin list may return addresses (org
  admins managing their own page) but mark the field redactable.
- `confirm_token` / `unsubscribe_token` are single-purpose, unguessable (≥128-bit random,
  e.g. `crypto/rand` → base64url). Confirm token consumed on use.
- **Rate-limit the public subscribe endpoint** to prevent using it as a mail-bomb relay
  (per-IP, low ceiling — reuse the HTTP rate-limiting middleware,
  `server/internal/middleware/`). Optionally cap subscribers per page.
- Double opt-in (goal 3) is itself the primary anti-abuse control: no mail flows to an
  address until that address clicks confirm.

## Files (high level)

| Area | Files |
|---|---|
| Model | `db/models/status_page_subscriber.go` |
| Migration | `db/sqlite/migrations/031_status_page_subscriber.{up,down}.sql` + same under `db/postgres/migrations/` |
| DB methods | `db/sqlite/status_page_subscriber.go`, `db/postgres/status_page_subscriber.go`, interface in `db/service.go` |
| Domain | `handlers/statussubscribers/{handler,service,handler_test,service_test}.go` |
| Mail templates | new file in `handlers/statussubscribers/` (HTML+Text builders) |
| Fan-out hook | `handlers/statusupdates/service.go` (`CreateStatusUpdate`) |
| Feed | `handlers/statuspages/` or `statussubscribers/` Atom renderer |
| Routes | `app/server.go` |
| Frontend | `web/status0` subscribe widget + feed link; `web/dash0` status-page edit subscriber list |

## Tests

### Backend (table-driven, `testify/require`, `t.Parallel()`)

- `statussubscribers/service_test.go`:
  - subscribe creates an unconfirmed row + a confirm token; second subscribe for the same
    (page,email,scope) re-uses / soft-undeletes rather than duplicating.
  - confirm sets `confirmed_at` and invalidates the confirm token; bad/expired token → error.
  - unsubscribe soft-deletes; bad token → error.
  - `ListConfirmedSubscribers` returns page-scoped + matching incident-scoped, excludes
    unconfirmed and soft-deleted.
- `statusupdates/service_test.go`: creating a status update enqueues mail to confirmed
  subscribers only (assert via a fake `email.Sender` capturing messages); a `resolved`
  update uses the resolved template; mail failure does not fail the update.
- DB parity: run the new DB-method tests against both sqlite and postgres harnesses
  (testcontainers) as existing status_update tests do.

### E2E Playwright (`web/dash0/e2e/` + status0)

- status0: subscribe widget submits, shows "check your inbox" state.
- dash0: status-page edit route shows the subscriber count + a remove action.

## Verification

1. `make build`, `make lint`, `make test` — green incl. new tests (both DB backends).
2. `make migrate` applies `031` cleanly on sqlite and postgres; `down` reverts.
3. `make dev-test`: on a public status page, subscribe → confirm mail captured (dev mail
   sink) → click confirm → publish a status update via `POST /status-updates` → subscriber
   mail captured with an unsubscribe link → click unsubscribe → no further mail.
4. `GET …/status-pages/:org/:slug/feed.xml` returns valid Atom with the recent updates.
5. `make test-dash` (Playwright) green.

## Implementation plan

1. **Model + migration 031** (both DB dirs) + DB methods (both backends) + interface;
   parity tests. `make migrate && make test`.
2. **Domain package** `statussubscribers` (subscribe/confirm/unsubscribe service +
   handler) + tokens + rate-limit on subscribe; service tests. `make test && make lint`.
3. **Mail templates** (confirm/opened/update/resolved) using `email.Sender`; unit tests
   with a fake sender.
4. **Fan-out hook** in `statusupdates.CreateStatusUpdate` (async, non-blocking) + test.
5. **Atom feed** endpoint + route registration in `server.go` for all new routes.
6. **Frontend**: status0 subscribe widget + feed link; dash0 admin subscriber list. E2E.
7. **Archive** — move spec to
   `specs/done/2026/05/2026-05-25-09-status-page-subscriber-notifications.md`.

## Priority

P1.2. No hard dependencies — reuses `email.Sender` and the public status-page +
status-update domains.

## Implementation Plan

> Concrete plan for this branch. NOTE: migration `031` is already taken
> (`031_result_duration_avg`); the new migration is **`032`** in both DB dirs.

1. **Model + migration 032 + DB methods + interface** — add
   `db/models/status_page_subscriber.go` (`StatusPageSubscriber`, `Scope` enum,
   `NewStatusPageSubscriber`). Add `032_status_page_subscriber.{up,down}.sql` to
   both `db/sqlite/migrations/` and `db/postgres/migrations/`. Implement DB methods
   in `db/sqlite/status_page_subscriber.go` + `db/postgres/status_page_subscriber.go`
   and declare them in `db/service.go` (`CreateSubscriber`,
   `GetSubscriberByConfirmToken`, `GetSubscriberByUnsubToken`, `ConfirmSubscriber`,
   `SoftDeleteSubscriber`, `ListConfirmedSubscribers`, `ListSubscribers`,
   `GetSubscriber`, `FindLiveSubscriber`). Add a DB-parity test block in
   `db/service_test.go` running against both sqlite and postgres harnesses.
2. **Domain package `statussubscribers`** — `service.go` (subscribe/confirm/
   unsubscribe/list/remove + `crypto/rand` base64url token generation, double
   opt-in, soft-undelete on re-subscribe) + `handler.go` (public subscribe/confirm/
   unsubscribe + authed list/remove + Atom feed). `service_test.go` + `handler_test.go`.
3. **Mail templates** — `mail.go` builder in `statussubscribers/` producing
   `email.Message` (HTML+Text) for `confirm`, `incident-opened`, `update`,
   `resolved`, each non-confirm message carrying the one-click unsubscribe link
   built from `config.Server.BaseURL`. Sent via `services.EmailSender.Send`.
4. **Fan-out hook** — add an optional `SubscriberNotifier` dependency to
   `statusupdates.Service`; `CreateStatusUpdate` dispatches mail to confirmed
   page-scoped + matching incident-scoped subscribers in a fire-and-forget
   goroutine that logs failures (never fails/blocks the update). Map `Kind` →
   template (resolved → resolved; first update for an incident → incident-opened;
   else → update). Test with a fake `email.Sender`.
5. **Routes** — register in `server.go`: two public routes outside `RequireAuth`
   (`POST /orgs/:org/status-pages/:statusPageUid/subscribers`,
   `GET /public/status-subscribers/{confirm,unsubscribe}`), the Atom feed
   (`GET /status-pages/:org/:slug/feed.xml`), and two authed admin routes under
   `RequireAuth` (list + delete). The global per-IP rate-limit middleware already
   covers all `/api/v1/` traffic; the public subscribe endpoint inherits it, and
   double opt-in is the primary anti-abuse control.
6. **Frontend** — `web/status0`: subscribe widget (email → "check your inbox")
   + `feed.xml` link on the public page. `web/dash0`: read-only subscriber list
   with count + `Trash2` remove action on the status-page edit route. Playwright E2E.
7. **Archive** — move spec to `specs/done/2026/05/`.
