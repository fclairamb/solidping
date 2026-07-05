# Email rework — unified design, dashboard links, per-check unsubscribe

## Context

SolidPing's emails are split across two generations of infrastructure and it
shows. Transactional emails (registration, password reset, invitation) render
through a proper pipeline — `html/template` with block inheritance over
`base.html`, CSS inlined by premailer, multipart text+HTML. Incident emails —
the ones customers actually see day to day — bypass all of it: they are
hand-built `fmt.Sprintf` HTML strings with ad-hoc inline styles, no branding,
no shared layout, and no way back into the product except the ack magic-link.

Three goals, in the user's words:

1. A much better overall design (inlined CSS and such) for **all** emails.
2. Links to the interface within emails.
3. Possibly allow subscribe/unsubscribe on per-check emails.

## Current state (verified 2026-07-05; re-verify at build)

- **Two rendering paths.** Auth emails use the formatter
  (`server/internal/email/formatter.go`): templates embedded from
  `server/internal/email/templates/`, subject from a `{{define "subject"}}`
  block, HTML post-processed by `go-premailer` to inline CSS. Incident emails
  build raw HTML/text in Go string literals —
  `buildIncidentCreatedHTML/Text`, `...Resolved...`, `...Escalated...`,
  `...Reopened...` in `server/internal/notifications/email.go:278-356` — with
  `style="..."` attributes sprinkled inline and no shared wrapper.
- **Templates directory** already holds `base.html` (header/footer wrapper,
  480px responsive table layout), `registration.html`, `password-reset.html`,
  `invitation.html`, plus `welcome.html`, `password-changed.html`, and
  `membership_request_*.html` (some not yet wired to a flow), and an
  `incident.html` stub.
- **Transport** is fine and out of scope: `go-mail` SMTP with
  TLS/STARTTLS/auth via `SP_EMAIL_*` config
  (`server/internal/config/config.go:296-307`), multipart/alternative with
  plaintext-first ordering (pinned by `TestSetBody_MultipartOrdering`).
- **Links today.** Incident emails carry only the ack magic-link
  (`buildAckURL`, `notifications/email.go:35-45`) on ackable events
  (created/escalated/reopened), built from `SP_SERVER_BASE_URL` + an HMAC
  token from `server/internal/incidentlinks/magiclink.go` (payload
  `incident_uid|email|exp`, signed with `Auth.JWTSecret`, 7-day TTL). No link
  to the incident page, the check page, or the dashboard anywhere. Auth
  emails do link into dash0 (confirm/reset/invite URLs).
- **Recipients** are a bare list: `Integration.Settings["to"]` (JSONB array
  of addresses) on the `email` integration
  (`server/internal/db/models/integration.go:65-86`), attached to checks via
  the checkchannels handlers. Every dispatch is audited per recipient in
  `incident_notifications` with a `skipped` status already available
  (`server/internal/db/models/incident_notification.go:33-59`). There is no
  per-recipient preference or unsubscribe mechanism of any kind.
- **Tests** pin sender behavior (`email/sender_test.go`), recipient
  resolution and delivery-artifact capture
  (`notifications/email_test.go`), and magic-link sign/verify round-trips
  (`incidentlinks/magiclink_test.go`).

## Design decisions

### D1 — One rendering pipeline for all emails

Kill the string-builder path. Incident emails become templates under
`server/internal/email/templates/` rendered through the existing formatter,
same as auth emails:

- `incident-created.html`, `incident-resolved.html`,
  `incident-escalated.html`, `incident-reopened.html` (replacing the stub
  `incident.html`), each with `{{define "subject"}}` (keeping today's
  `[DOWN]` / `[RECOVERED]` / `[ESCALATED]` / `[REOPENED]` subjects and the
  existing optional `subject_prefix`).
- Each template also defines a `{{define "text"}}` block so the plaintext
  alternative is template-generated too — the formatter grows a text-render
  path, and multipart plaintext-first ordering stays pinned.
- `notifications/email.go` shrinks to data assembly: build a view-model
  (check name/type/URL, incident times, duration, relapse/failure counts,
  ack URL, dashboard URLs) and call the formatter. The
  `buildIncident*HTML/Text` functions are deleted.

### D2 — Redesigned base layout

`base.html` gets a real design pass, applied automatically to every email:

- Branded header (SolidPing wordmark), light-neutral body card, muted footer
  with org name and link section.
- A **status banner** block templates can invoke: red (down), green
  (recovered), amber (escalated/reopened) — color as background on a full
  width bar, not text color alone.
- A shared **button** partial (used by ack, "View incident", and the auth
  CTAs) so all emails get identical, bulletproof buttons (table-based, works
  in Outlook).
- All styling in a `<style>` block in `base.html`; premailer keeps inlining
  it, so templates stay readable and clients stay compatible. 480px
  responsive behavior preserved.
- Existing auth templates are restyled onto the new blocks in the same pass —
  "all emails" means all: registration, password-reset, invitation, welcome,
  password-changed, membership-request templates too, even the not-yet-wired
  ones (wiring them stays out of scope).

### D3 — Dashboard links in every email

Every email links back into the interface, built from
`SP_SERVER_BASE_URL`:

- Incident emails: primary CTA **"View incident"** →
  `{base}/dash0/orgs/{org}/incidents/{uid}`, plus a link on the check name →
  the check detail page (verify exact dash0 route shapes at build). Ack
  magic-link button stays alongside on ackable events.
- Footer on all emails: link to the dashboard (`{base}/dash0`) and to the
  docs (`{base}/docs`).
- `SP_SERVER_BASE_URL` always resolves (koanf default
  `http://localhost:4000`, `config.go:605`), so links render
  unconditionally — no "unset base URL" degradation path.

### D4 — Per-recipient unsubscribe for check/incident emails

The "possibly" part — in scope, but sequenced last (D1–D3 must not depend
on it).

- **Suppression table**, new migration: `email_suppressions`
  (`uid`, `organization_uid`, `email`, `check_uid` NULLable — NULL means all
  checks in the org —, `created_at`, `source` = `link|header|dashboard`).
  Unique on (org, email, check_uid).
- **Signed unsubscribe links.** Generalize `incidentlinks` into a
  purpose-tagged signed-link helper: the HMAC payload gains a purpose prefix
  (`ack|...` vs `unsub|...`) so tokens are domain-separated and an ack token
  can never be replayed as an unsubscribe (and vice versa). Existing ack
  tokens keep verifying during a deprecation window, or ack re-mints with the
  prefix in the same release (preferred — tokens are short-lived).
  Unsubscribe payload: `unsub|org|email|check_uid|exp`, TTL **90 days**
  (unlike ack, these get clicked long after delivery; stateless tokens mean
  no revocation, same trade-off as ack, acceptable because the action is
  low-risk and reversible).
- **Headers + footer link.** Incident/alert emails get
  `List-Unsubscribe: <{base}/unsubscribe?token=...>` and
  `List-Unsubscribe-Post: List-Unsubscribe=One-Click` (RFC 8058) per
  recipient — this requires the per-recipient send path for all incident
  events, not just ackable ones (resolved emails are broadcast today; they
  become per-recipient too). The email footer gets an "Unsubscribe from
  alerts for this check" link with the same token.
- **Endpoints.** `POST /unsubscribe?token=...` (RFC 8058 one-click, no auth,
  idempotent) inserts the suppression. `GET /unsubscribe?token=...` renders a
  minimal public confirmation page (served by the Go binary like
  `/status0`, no dash0 auth) offering "this check only" / "all alert emails
  for this org" and a re-subscribe undo (deletes the row while the token is
  valid).
- **Enforcement.** Recipient resolution in `notifications/email.go` filters
  suppressed addresses per (org, email, check) before sending; a recipient
  dropped this way gets an `incident_notifications` audit row with status
  `skipped` and a delivery detail naming the suppression. Transactional
  emails (registration, reset, invitation, password-changed) are **never**
  suppressed and carry no List-Unsubscribe headers — they are
  account-critical, not subscriptions.
- **Dashboard surface.** The email integration page lists current
  suppressions for its recipients (org, email, scope, date) and lets an org
  admin remove one (re-subscribe). API: `GET/DELETE
  /api/v1/orgs/:org/email-suppressions[...]` following the REST conventions
  (`{"data": [...]}`, `$uid` paths). Follow the design-reference page for
  the table + destructive-action conventions.

### D5 — Dev preview route

Iterating on email design over SMTP round-trips is miserable. Add a
test/dev-only route (gated on `SP_RUNMODE=test`, like other test-mode
surfaces) that renders any template with fixture data in the browser:
`GET /api/mgmt/email-preview/{template}?format=html|text`. Not compiled
out — just 404 outside test mode, mirroring existing gating.

## Non-goals

- Changing the transport: no ESP/API providers (SES, Postmark…), no
  bounce/complaint webhooks, no delivery tracking beyond the existing
  Message-ID capture.
- Digest/summary emails, quiet hours, or a full per-user notification
  preference center — suppression list only.
- Dark-mode-specific email variants (the palette should merely not break in
  dark-mode clients).
- Wiring the currently-unwired templates (welcome, password-changed,
  membership-request) to new flows — they only get restyled.
- Per-recipient preferences for non-email channels (Slack, webhook, …).

## Acceptance criteria

1. No incident email HTML/text is built via string concatenation:
   `buildIncident*` functions are gone and all four incident events render
   through the formatter from embedded templates, with premailer-inlined CSS
   and template-generated plaintext alternatives (multipart ordering test
   still green).
2. Every shipped template (incident ×4 + auth/transactional) renders on the
   new `base.html` with the shared header/footer/button/status-banner blocks;
   rendering each template with fixture data in tests produces valid HTML
   with zero `<style>` blocks remaining in the body (fully inlined) and no
   unresolved template variables.
3. Incident emails contain a working "View incident" URL and check link
   derived from `SP_SERVER_BASE_URL` in both HTML and text parts. Ack
   behavior is unchanged on ackable events.
4. A recipient can one-click unsubscribe (RFC 8058 POST) and via the GET
   confirmation page; the suppression row is created with the right scope,
   and subsequent incident emails for that (org, email, check) are skipped
   with an audit row (status `skipped`), while other recipients still
   receive. Re-subscribe (undo link and dashboard delete) restores delivery.
5. Unsubscribe tokens are purpose-tagged: an ack token presented to
   `/unsubscribe` is rejected, and an unsubscribe token presented to the ack
   endpoint is rejected (tests for both directions).
6. Transactional emails carry no List-Unsubscribe headers and are delivered
   to suppressed addresses.
7. Tests: formatter text-block rendering; per-template golden/smoke render
   tests; suppression filtering in recipient resolution (table-driven);
   signed-link purpose separation; unsubscribe endpoints (one-click,
   confirmation page, idempotency, expired token); dashboard suppression
   list/delete E2E (Playwright) if a page is added.
8. `make lint` and `make test` pass; dash0 lint introduces no new errors.

## Open questions

- Exact dash0 route for incident detail (deep link target in D3) — confirm
  at build time and use the canonical route, not a guess.
- Whether the GET unsubscribe page should offer "all checks" scope
  immediately or ship check-only first (D4 leans: offer both, the table
  models it already).

## Implementation Plan

*(added 2026-07-05, resolving both open questions from the actual codebase)*

### Open questions — resolved

1. **Dash0 incident/check routes.** Confirmed by reading
   `web/dash0/src/routes/orgs/$org/` (TanStack Router file-based routing):
   `incidents.$incidentUid.tsx` → `/dash0/orgs/{org}/incidents/{uid}`, and
   `checks.$checkUid.index.tsx` → `/dash0/orgs/{org}/checks/{slug}` (check
   pages resolve by slug, not uid — see `checkDashURL` below). The spec's
   assumed shape was exactly right. Better still: `server/internal/notifications/slack.go:174-189`
   already has `checkDashURL(baseURL, orgSlug, check) string` and
   `incidentDashURL(baseURL, orgSlug, incident) string` helpers in the *same*
   `notifications` package that produce these exact URLs (used for Slack
   hyperlinks today). D3 reuses these two helpers verbatim from
   `notifications/email.go` — no new URL-building code, no risk of the two
   channels drifting.
2. **GET unsubscribe scope.** Ship both scopes immediately (per the spec's
   lean) — "this check only" and "all alert emails for this org" as two
   buttons/options on the same confirmation page. The `email_suppressions`
   table already models both via nullable `check_uid`, so there's no
   incremental cost to shipping both at once, and a check-only-first release
   would just require a second round of UI work later for no benefit.

### Architecture findings that shape the plan

- **Formatter interface must change.** `email.Formatter.Format` currently
  returns `(subject, html string, err error)` with an explicit doc-comment
  that plaintext is intentionally not produced. D1 needs a text alternative
  for incident emails (multipart ordering test must stay green), so the
  interface grows to `Format(name string, data any) (subject, html, text string, err error)`.
  `text` is rendered from an optional `{{define "text"}}` block; when a
  template has no such block (all the current auth templates), `text` is
  `""` and callers behave exactly as before (HTML-only send). This keeps
  `job_email.go`'s template-driven path backward compatible.
- **Job dispatch architecture (traced through `job_notification.go`).**
  `NotificationJobRun.Run` loads connection/incident/check, builds
  `notifications.Payload`, and calls `sendAndAudit` →
  `sender.Send(ctx, jctx, payload)` (this is `EmailSender.Send`) → on
  success/failure updates the **job-level** audit row via
  `jctx.DBService.MarkIncidentNotificationSentByJob` /
  `MarkIncidentNotificationFailedByJob`. Critically, `jctx.DBService` (type
  `db.Service`, dual-backend-implemented) is reachable from inside
  `EmailSender.Send` itself (it already receives `jctx`), and
  `db.Service.CreateIncidentNotification(ctx, *models.IncidentNotification) error`
  is a real interface method implemented on both
  `internal/db/postgres/incident_notification.go` and
  `internal/db/sqlite/incident_notification.go`. So: per-recipient `skipped`
  rows for suppressed addresses are inserted **directly by
  `EmailSender.Send`** via `jctx.DBService.CreateIncidentNotification(ctx,
  models.NewSkippedIncidentNotification(orgUID, incidentUID, eventType,
  models.IncidentNotificationSourceCheckConnection, "suppressed: <email>
  (scope)", nil, nil))` — one extra row per suppressed recipient, alongside
  (not instead of) the existing job-level sent/failed row that
  `sendAndAudit` still writes for the overall dispatch. No changes needed to
  `job_notification.go` itself.
- **Route registration pattern for public (unauthenticated) endpoints.**
  Confirmed in `server/internal/app/server.go`: `api := mainGroup.NewGroup("/api/v1")`
  is itself unauthenticated; sub-groups like `orgIncidents := api.NewGroup(...).Use(authMiddleware.RequireAuth)`
  opt INTO auth. The existing magic-link ack endpoint is registered directly
  on `api` (not on `orgIncidents`) at server.go:758:
  `api.GET("/orgs/:org/incidents/:uid/ack", incidentsHandler.AcknowledgeIncidentByLink)`.
  The new `POST /unsubscribe` and `GET /unsubscribe` (RFC 8058 + confirmation
  page) follow the identical pattern: registered directly on `mainGroup`
  (top-level, alongside `/docs`, `/openapi`, `/status0`) since they're
  cross-org (org is embedded in the token, not the URL path) — no bearer
  auth, no CORS/org-scoping needed. The email-preview dev route
  (`GET /api/mgmt/email-preview/{template}`) is gated exactly like the
  existing `/api/test/*` cluster at server.go:1136:
  `if s.config.RunMode == "test" { ... }`, registered on `api` (also
  unauthenticated — matches other test-mode surfaces which have no auth
  requirement either).
- **Migration versioning.** `002_v0_2_0` already shipped (CHANGELOG.md /
  `.release-please-manifest.json` both show `0.2.0` released 2026-07-04, one
  day before this build) — it is a frozen, released consolidated migration
  and must not be edited. The new `email_suppressions` table goes into a
  fresh pair: `003_v0_2_1.up.sql` / `003_v0_2_1.down.sql` on both backends
  (migration discovery is `migrate.NewMigrations().Discover(migrationsFS)`
  via `//go:embed migrations/*.sql` in both `postgres.go` and `sqlite.go` —
  pure filename-glob discovery, so any correctly-numbered new pair is picked
  up automatically; the actual release version is decided later by
  release-please and does not need to match the filename literally, it's
  just a sequential label like the existing files).
- **DDL conventions confirmed from `002_v0_2_0.up.sql`/`.down.sql` (both
  backends), using the `discovered_checks` table added there as the most
  recent "new table" precedent**:
  - Postgres: `uid uuid primary key default gen_random_uuid()`,
    `organization_uid uuid not null references organizations(uid)`,
    `created_at timestamptz not null default now()`, JSON columns as
    `jsonb`, `comment on table/column` for documentation, partial unique
    indexes with `where deleted_at is null` (not applicable here — no soft
    delete on suppressions, they're just deleted on unsubscribe-undo).
  - SQLite: `uid text primary key` (uid generated in Go via `uuid.New()`,
    no DB-side default), `organization_uid text not null references organizations(uid)`,
    `created_at text not null default (datetime('now'))`, JSON columns as
    plain `text`, inline `--` comments (no `comment on`) since SQLite has no
    such statement.
  - Both backends: `create unique index ... on email_suppressions (organization_uid, email, check_uid)`.
    Note Postgres NULL semantics: a plain unique index treats each NULL
    `check_uid` as distinct, so two org-wide (`check_uid IS NULL`) rows for
    the same email would NOT collide under a naive unique index — need
    either (a) a partial unique index split in two (`... where check_uid is
    not null` plus a second `... where check_uid is null`), or (b) a
    sentinel non-null value. Going with (a): two unique indexes,
    `email_suppressions_check_scope_idx` (`organization_uid, email,
    check_uid) where check_uid is not null` and
    `email_suppressions_org_scope_idx` (`organization_uid, email) where
    check_uid is null`. SQLite unique indexes also treat NULL as distinct
    per-row (same underlying issue), so the same two-partial-index split is
    used on both backends for consistency (SQLite supports partial indexes
    via `WHERE` since 3.8.0, well within the bundled engine's version).
- **Bun model + db.Service methods needed**: new
  `server/internal/db/models/email_suppression.go` (`EmailSuppression`
  struct, `NewEmailSuppression` constructor, `EmailSuppressionSource*`
  consts for `link|header|dashboard`), plus `db.Service` interface additions
  `CreateEmailSuppression`, `ListEmailSuppressions(orgUID)`,
  `DeleteEmailSuppression(orgUID, uid)`, `IsEmailSuppressed(ctx, orgUID,
  email, checkUID *string) (bool, error)` (checks both the check-specific
  row and the org-wide NULL-check_uid row — a single query with `check_uid =
  ? OR check_uid IS NULL`), implemented in both
  `internal/db/postgres/email_suppression.go` and
  `internal/db/sqlite/email_suppression.go`, mirroring the existing
  `incident_notification.go` pair's structure exactly.
- **Recipient filtering enforcement point**: `EmailSender.Send` (in
  `notifications/email.go`) already resolves `emailAddresses` via
  `extractRecipients`. Insert filtering right after that resolution, before
  `sendPerRecipient`/broadcast branch: for each candidate address, call
  `jctx.DBService.IsEmailSuppressed(ctx, orgUID, addr, &checkUID)` (org UID
  and check UID come from `payload.Integration.OrganizationUID` /
  `payload.Check.UID`); suppressed addresses are removed from the send list
  and get a `skipped` audit row each (per the point above); if the filtered
  list is empty, `Send` returns nil (not `ErrNoValidRecipients` — that error
  is reserved for "nothing was ever configured", not "everyone
  unsubscribed"; the audit trail already explains why nothing went out).
  Transactional email paths (`job_email.go`'s `EmailJobRun`, and
  `auth/service.go`'s `enqueueEmail`) are untouched — they never call
  `EmailSender.Send`'s incident path, so suppression simply never applies to
  them, satisfying acceptance criterion 6 by construction (no filter to
  bypass, because there's no shared code path).
- **Per-recipient send for "resolved" events.** Today only
  created/escalated/reopened use `sendPerRecipient` (`canAckEvent`); resolved
  broadcasts once. D4 requires per-recipient List-Unsubscribe tokens even
  for resolved, so `canAckEvent`'s result is no longer the switch for
  "personalize per recipient" — a new `needsPerRecipientSend` concept
  (effectively: always true for the four incident event types, since all now
  need a per-recipient unsubscribe token; ack button rendering still gated
  separately by `canAckEvent` inside the per-recipient body-building step).
  Concretely: `Send` always calls a per-recipient path for the four incident
  event types; the ack button block is included only when `canAckEvent` is
  also true. The unrecognized-event `default` case in `buildEmailContent`
  stays broadcast (it's not one of the four modeled events and carries no
  unsubscribe semantics either — dead code path today, kept as a fallback).
- **Purpose-tagged signed links.** Generalize `incidentlinks` package: keep
  `Sign`/`Verify` names but add a `purpose` parameter (or, more minimally,
  fold the purpose into the payload as the FIRST field so the shape becomes
  `<purpose>|<rest>`) — going with a `SignWithPurpose(secret, purpose,
  parts... string, exp time.Time) string` / `VerifyWithPurpose(secret,
  expectedPurpose string, parts ...matcher, token string)` generalization is
  overkill for two call shapes (ack: incident+email; unsub: org+email+check),
  so instead: add explicit purpose constants `PurposeAck = "ack"`,
  `PurposeUnsubscribe = "unsub"`, keep `Sign`/`Verify` for ack (re-minting
  its payload as `ack|incident_uid|email|exp` — a strict superset of the
  purpose-tagging requirement, achieved by prefixing the existing payload
  string before hashing) and add sibling `SignUnsubscribe`/`VerifyUnsubscribe`
  for `unsub|org_slug|email|check_uid|exp` (`check_uid` empty string = org-wide).
  Both verifiers check the leading segment matches their expected purpose
  literal and reject (new `ErrPurposeMismatch` sentinel) otherwise — this is
  what makes cross-purpose replay impossible (test both directions per
  acceptance criterion 5). No dual-verification deprecation window — ack
  tokens are 7-day TTL, so re-minting with the new prefix in this release is
  safe per the spec's own preference (old un-prefixed tokens outstanding at
  deploy time will fail to verify, which is an acceptable, tiny blast radius
  consistent with the existing "rotating JWT secret invalidates everything"
  threat model already documented in the package).
- **Frontend placement for D4 dashboard surface.** The existing email
  integration edit page (`web/dash0/src/routes/orgs/$org/integrations.$integrationUid.tsx`)
  already has a `RecentNotificationsSection` card pattern (fetch + Table +
  loading/empty/error states) — but suppressions are **org-scoped**, not
  integration-scoped (a suppression has no `integration_uid`, only
  `organization_uid` + `email` + optional `check_uid`), so a single email
  integration's edit page is the wrong home for an org-wide list — an org can
  have multiple email integrations. Per the "use your judgment on minimal
  correct placement, must be reachable from UI" clause: add a
  `SuppressionsSection` card to the email integration's detail page
  (`integrations.$integrationUid.tsx`, gated on `integration.type ===
  "email"`) showing ALL org suppressions (not just ones tied to this
  integration's recipients) — reachable, minimal, no new route, and
  contextually the only place a user configuring email alerts would look for
  "who's opted out." Table columns: email, scope (check name or "All
  checks"), created date, and a `Trash2` destructive icon-button per row
  (re-subscribe) — mirrors the `RecentNotificationsSection` component
  structure and the design-reference destructive-row pattern (ghost icon
  button + inline delete, no confirmation dialog needed since re-subscribing
  is low-risk and reversible — consistent with the spec calling the
  unsubscribe action itself "low-risk and reversible"). New hooks
  `useEmailSuppressions(org)` / `useDeleteEmailSuppression(org)` added to
  `web/dash0/src/api/hooks.ts`, modeled directly on the existing
  `useTokens(org)` / token-delete pair (`{"data": [...]}` unwrap, simple
  `DELETE /api/v1/orgs/:org/email-suppressions/:uid`).
- **D5 dev preview route** lives alongside the existing `testapi` handler
  cluster (`internal/handlers/testapi/handler.go` already has a
  `"welcome.html", true` style template-name mapping used by
  `GenerateData`) — new handler renders any of the 9 (soon 12, minus the
  incident.html stub = 12 total: 4 incident + registration + password-reset
  + invitation + welcome + password-changed + membership_request_new +
  membership_request_decision + base itself is not directly renderable)
  shipped templates with a fixture-data table keyed by template name,
  formats via `format=html|text` query param using the same
  `email.Formatter.Format` the real send path uses (so the preview is
  provably identical rendering, not a reimplementation), 404s via
  `base.WriteError(w, http.StatusNotFound, ...)` when `RunMode != "test"`.

### File-by-file plan

**Backend — D1/D2/D3 (templates + view-model, can be built and visually
iterated using D5 without SMTP):**
- `server/internal/email/email.go`: widen `Formatter` interface to also
  return `text string`.
- `server/internal/email/formatter.go`: add `renderText` mirroring
  `renderSubject` (looks up `{{define "text"}}`, executes with data,
  returns `""` on no-block — NOT an error).
- `server/internal/email/templates/base.html`: full redesign — branded
  header, card body, muted footer with org name + dashboard/docs links,
  `{{block "statusbanner" .}}{{end}}` full-width color bar
  (red/green/amber), `{{define "button"}}`-style table-based button
  partial parameterized by href/label/variant, kept in a `<style>` block
  (premailer inlines unchanged). 480px media query preserved and extended.
- New `server/internal/email/templates/incident-created.html`,
  `incident-resolved.html`, `incident-escalated.html`,
  `incident-reopened.html` — each defines `subject`, `content` (banner +
  check-name-as-link + details table + optional ack button + optional
  unsubscribe footer line), and `text`. Delete `incident.html` stub.
- Restyle `registration.html`, `password-reset.html`, `invitation.html`,
  `welcome.html`, `password-changed.html`, `membership_request_new.html`,
  `membership_request_decision.html` onto the new base blocks (button
  partial, footer). Add `{{define "text"}}` to each (currently HTML-only —
  extending them costs nothing and is consistent with "all emails").
- `server/internal/notifications/email.go`: delete all `buildIncident*HTML/Text`
  functions (lines ~278-503); `buildEmailContent` becomes a view-model
  builder that calls `jctx.Services.EmailFormatter.Format(templateName,
  viewModel)` — needs `EmailFormatter` threaded into `EmailSender` (it's
  already on `jctx.Services`, just wire the call). View-model includes:
  check name/type, `checkDashURL(...)` link, incident times (formatted),
  duration (resolved), relapse/failure counts, ack URL (existing
  `buildAckURL`, re-minted with `PurposeAck`), `incidentDashURL(...)`,
  dashboard root + docs footer links (`{base}/dash0`, `{base}/docs`),
  unsubscribe URL + List-Unsubscribe header value (D4).
- `server/internal/notifications/sender.go` / SMTP path: thread
  `List-Unsubscribe` / `List-Unsubscribe-Post` headers through
  `email.Message` (new fields) → `SMTPSender.buildMessage` sets them via
  `mailMsg.SetGenHeader`.

**Backend — D4 (sequenced after D1-D3 land and are green):**
- `server/internal/db/postgres/migrations/003_v0_2_1.{up,down}.sql` and
  `server/internal/db/sqlite/migrations/003_v0_2_1.{up,down}.sql`:
  `email_suppressions` table per the DDL conventions above.
- `server/internal/db/models/email_suppression.go`: model + constructor +
  source consts.
- `server/internal/db/postgres/email_suppression.go` +
  `server/internal/db/sqlite/email_suppression.go`: Create/List/Delete/
  IsSuppressed, added to the `db.Service` interface in
  `server/internal/db/service.go`.
- `server/internal/incidentlinks/magiclink.go`: add `Purpose` consts,
  re-mint `Sign`/`Verify` payload with the `ack|` prefix, add
  `SignUnsubscribe`/`VerifyUnsubscribe` + `ErrPurposeMismatch`. Update
  `server/internal/handlers/incidents/magiclink.go` re-exports if the
  signature changes.
- New `server/internal/handlers/unsubscribe/` (handler+service, mirrors the
  smallest existing simple handler+service pair): `POST /unsubscribe`
  (one-click, idempotent insert-or-noop), `GET /unsubscribe` (renders a
  minimal static-style HTML confirmation page à la
  `incidents/ack_html.go`'s `renderAckPage` pattern, offering both scope
  buttons + a re-subscribe undo form). Registered on `mainGroup` directly
  (server.go), not `api`, not org-scoped in the URL (org comes from the
  token).
- `server/internal/notifications/email.go`: suppression filter step (see
  above) + skipped-audit-row insertion.
- New `server/internal/handlers/emailsuppressions/` (handler+service):
  `GET /api/v1/orgs/:org/email-suppressions`, `DELETE
  /api/v1/orgs/:org/email-suppressions/:uid`, registered on an
  authenticated `api.NewGroup("/orgs/:org/email-suppressions").Use(authMiddleware.RequireAuth)`
  group in server.go, `{"data": [...]}` wrapping.

**Backend — D5:**
- New `server/internal/handlers/testapi/` addition (or sibling small
  handler): `GET /api/mgmt/email-preview/{template}?format=html|text`,
  gated `if s.config.RunMode == "test"` alongside the existing test-route
  cluster at server.go:1136, fixture-data table keyed by template name,
  calls the real `email.Formatter.Format`.

**Frontend — D4 dashboard surface:**
- `web/dash0/src/api/hooks.ts`: `useEmailSuppressions(org)` +
  `useDeleteEmailSuppression(org)`, modeled on `useTokens`/token-delete.
- `web/dash0/src/routes/orgs/$org/integrations.$integrationUid.tsx`: new
  `SuppressionsSection` card (gated on `integration.type === "email"`),
  `Table` + per-row `Trash2` destructive icon button, empty/loading/error
  states mirroring `RecentNotificationsSection`.
- `web/dash0/e2e/`: new Playwright spec covering list + delete
  (re-subscribe) for the suppression section, following existing
  integration-page E2E conventions.

### Sequencing

1. D1 (formatter text path + delete string-builders + new incident
   templates) — commit per template.
2. D2 (base.html redesign + restyle auth templates) — can be developed
   visually via D5 preview route, built early to unblock iteration.
3. D3 (dashboard links via `checkDashURL`/`incidentDashURL` reuse in the
   view-model).
4. D5 preview route (useful throughout D1-D3, but written as its own
   commit once the template set stabilizes enough to have fixture data for
   all of them).
5. D4 last, in the order: migration → model → db.Service methods →
   magiclink purpose-tagging (with both-direction tests) → per-recipient
   resolved-event send + List-Unsubscribe headers → unsubscribe
   endpoints → recipient-filtering enforcement + skipped audit rows →
   REST CRUD + frontend section + E2E.
