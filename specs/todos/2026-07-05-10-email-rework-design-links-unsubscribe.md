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
