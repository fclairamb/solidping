---
model: sonnet
effort: high
---

# Several emails bypass the shared template system, and incident emails never show the incident number

## Problem

We have a proper email rendering pipeline — `server/internal/email/formatter.go`
renders `templates/*.html` against the shared `base.html` (branding, status
banner, button/details-table partials, subject + HTML + plaintext blocks), and
`server/internal/handlers/emailpreview/` lets us iterate on them visually. The
four modeled incident events already go through it
(`internal/notifications/email.go` → `incident-created/resolved/escalated/reopened.html`).

But a handful of emails still bypass the formatter entirely and go out as
hand-rolled strings:

1. **Escalation-policy email** —
   [job_escalation_step.go:1163](server/internal/jobs/jobtypes/job_escalation_step.go:1163).
   Plain text only, no HTML, and the subject/body address the incident by its
   raw 36-char UUID: `[escalation] incident <uuid> requires attention`. This is
   the worst offender: it's a customer-facing alert. (The code comment even says
   "V1 body is plain text — richer formatting can come later".)
2. **Admin "test email"** —
   [system/service.go:309](server/internal/handlers/system/service.go:309).
   Hand-rolled `<h2>…</h2><p>…</p>` HTML, no base template.
3. **Member paging nudge** ("set up your alert notifications") —
   [members/provisioning.go:212](server/internal/handlers/members/provisioning.go:212).
   Hand-concatenated HTML string, no base template.
4. **User-notification test email** —
   [usernotifications/senders.go:23](server/internal/handlers/usernotifications/senders.go:23).
   Plain text only, no HTML part at all.
5. **Status-page subscriber emails** (confirm, incident-opened, update,
   resolved) —
   [statussubscribers/mail.go](server/internal/handlers/statussubscribers/mail.go).
   A whole parallel hand-rolled HTML builder (`buildHTML`/`buildText`) with its
   own inline styles, duplicating what `base.html` already provides.

Separately, **incident emails never show the incident number**. Incidents carry
a short per-org reference (`models.Incident.Number`, rendered as `#42` in the
dashboard, Slack and Telegram — see
[incident.go:31](server/internal/db/models/incident.go:31)), but:

- `buildIncidentViewModel`
  ([notifications/email.go:465](server/internal/notifications/email.go:465))
  never passes `payload.Incident.Number`, so none of the four
  `incident-*.html` templates can show it — subjects read `[DOWN] <check> is
  down` and the details table lists check/type/time but no reference a human
  could quote back.
- The escalation-policy email (item 1) shows the UUID instead of the number.

## Proposal

**A. Route every bypassing email through the shared formatter.**

For each of the five call sites above, create a template in
`server/internal/email/templates/` (with `subject`, `content`, and `text`
blocks, extending `base.html` like the existing ones) and render it via the
`email.Formatter` service instead of building strings inline:

- `escalation.html` — for the escalation-policy email. Reuse the visual
  language of `incident-escalated.html` (statusbanner variant `escalated`,
  details table, dashboard button). View model needs check name, incident
  number, incident dash URL, started-at, and the escalation step context the
  job already has. Note the job currently only has `jctx.Services.EmailSender`;
  it must also use `jctx.Services.EmailFormatter` (nil-guard like
  `notifications/email.go` does).
- `test-email.html` — shared by both test sends (2 and 4). Keep the subjects
  distinct if desired via template data, but one template is enough.
- `paging-nudge.html` — for the member provisioning nudge (needs org name and
  the notifications settings link).
- `status-subscriber-confirm.html` and `status-subscriber-update.html` — for
  the statussubscribers package; the update template covers the
  incident-opened / update / resolved kinds via a label/variant field, mirroring
  today's `MailKind.label()` switch. Keep the existing subjects, the
  unsubscribe footer line, and the `white-space:pre-wrap` treatment of the
  markdown body (it is escaped text today — do not start interpreting HTML).
  The statussubscribers handler/notifier currently hold only an `email.Sender`;
  inject the `email.Formatter` alongside it.

Delete the now-dead `buildHTML`/`buildText`/`writeConfirmBody`/`writeUpdateBody`
builders once migrated.

**B. Add the incident number to every incident-related email.**

- Add `"IncidentNumber": payload.Incident.Number` to `buildIncidentViewModel`
  ([notifications/email.go:465](server/internal/notifications/email.go:465)).
- Surface it in all four `incident-*.html` templates the same way the other
  surfaces do (`#42`): in the subject (e.g. `[DOWN] <check> is down (#42)`) and
  as a `Incident` / `#42` row in the details table (linking to `IncidentURL`
  where the templates already have it).
- The new `escalation.html` (A) uses `#<number>` in subject and body instead of
  the UUID.
- Status-subscriber update emails: the fan-out event
  (`statussubscribers.UpdateEvent`) carries `IncidentUID` but not the number —
  plumb the number through the event if it is cheaply available at the emit
  site; if not, leave it out there and note it in the spec archive (public
  status-page subscribers see the status page's own wording, so the org-internal
  `#42` is nice-to-have, not required).

**C. Tests and preview fixtures.**

- Extend `emailpreview/fixtures.go` with fixtures for every new template so
  `/api/.../email-preview` can render them, and keep its supported-template
  list authoritative.
- Update `formatter_test.go` / the affected package tests: assert the new
  templates render (subject, HTML contains the incident number where
  applicable, text block non-empty), and fix existing tests that assert the old
  hand-rolled strings (statussubscribers tests, escalation job tests,
  usernotifications test-send tests).

## Acceptance criteria

1. No production email is built by string concatenation anymore — every send
   site formats through `email.Formatter` (grep for `email.Message{` should
   show only sites whose Subject/HTML/Text come from `Format(...)`).
2. All four incident templates + the escalation email show the incident number
   as `#N` in both subject and body (HTML and text parts).
3. The escalation-policy email has an HTML part and no longer exposes the raw
   incident UUID as the human-facing reference.
4. Every template (new and old) has a fixture in `emailpreview` and renders
   without error there.
5. Existing List-Unsubscribe behavior for status-subscriber and per-recipient
   incident emails is unchanged.
