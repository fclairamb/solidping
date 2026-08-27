---
model: sonnet
effort: medium
---

# Incident emails render the bare check name, so mail clients auto-link the hostname instead of the check page

## Problem

A received escalation email shows the check ("cup.acme.com 443") twice: once in
the intro paragraph, once in the details table's "Check" row. The paragraph is a
real link to the dashboard (the templates wrap it in `<a href="{{.CheckURL}}">`
when `CheckURL` is set), but the table row renders the bare name:

```
<tr><td class="label">Check</td><td class="value">{{.CheckName}}</td></tr>
```

Checks are very often named after their target host, so Gmail (and most other
clients) auto-linkifies the hostname-looking part of the bare text into
`https://cup.acme.com` — a link straight to the target host, which in an
incident email is precisely the thing that is down. The reader gets a
plausible-looking link that is strictly worse than the actual check page link.

Every incident template has the same unlinked table row (and the `<h1>` /
plain-text parts render the bare name too):

- [escalation.html:9](server/internal/email/templates/escalation.html:9)
- [incident-created.html:9](server/internal/email/templates/incident-created.html:9)
- [incident-resolved.html:20](server/internal/email/templates/incident-resolved.html:20)
- [incident-acknowledged.html:13](server/internal/email/templates/incident-acknowledged.html:13)
- [incident-reopened.html:9](server/internal/email/templates/incident-reopened.html:9)
- [incident-escalated.html:9](server/internal/email/templates/incident-escalated.html:9)
- [incident-comment.html:10](server/internal/email/templates/incident-comment.html:10)
- [incident-burn-created.html:29](server/internal/email/templates/incident-burn-created.html:29) ("Detected on" row)

`CheckURL` is already available to all of them: the shared incident email view
model sets it ([email.go:588](server/internal/notifications/email.go:588) via
`checkDashURL`, [slack.go:243](server/internal/notifications/slack.go:243)) and
the escalation job sets it too
([job_escalation_step.go:1417](server/internal/jobs/jobtypes/job_escalation_step.go:1417)
via `escalationCheckURL`,
[job_escalation_step.go:1468](server/internal/jobs/jobtypes/job_escalation_step.go:1468)).

A second wrinkle: both URL builders link by **slug**
(`/dash0/orgs/{org}/checks/{slug}`) and return `""` when `check.Slug` is nil,
which is what the report's "UUID format" ask points at. The dash0 route param is
`$checkUid` and the backend resolves `GetCheckByUidOrSlug`
([handler.go:346](server/internal/handlers/checks/handler.go:346)), so a
UID-based link works today, survives a check rename between the email being sent
and the reader clicking it, and never hits the nil-slug fallback that leaves the
name unlinked.

## Proposal

1. **Link the check name everywhere it appears in the HTML bodies.** In each
   template above, replace the bare `{{.CheckName}}` in the details-table row
   with the same conditional anchor pattern the intro paragraphs already use:
   `{{if .CheckURL}}<a href="{{.CheckURL}}">{{.CheckName}}</a>{{else}}{{.CheckName}}{{end}}`.
   An explicit anchor is what stops the client from injecting its own
   auto-link. Do the `<h1>` headings the same way if styling allows; the
   subject line cannot carry a link and stays as-is.

2. **Build the check URL from the UID, not the slug.** Change `checkDashURL`
   ([slack.go:243](server/internal/notifications/slack.go:243)) and
   `escalationCheckURL`
   ([job_escalation_step.go:1468](server/internal/jobs/jobtypes/job_escalation_step.go:1468))
   to use `check.UID` (`/dash0/orgs/{org}/checks/{uid}`), dropping the
   nil-slug early return. This also upgrades the Slack/Teams/Discord/PagerDuty
   notifiers that share `checkDashURL`, which is desirable for the same
   rename-safety reason.

3. **Plain-text parts**: clients auto-link hostnames in text bodies too and no
   anchor can prevent it there; instead make the real link win by adding an
   explicit `Check: {{.CheckURL}}` (or appending the URL after the name) in the
   `{{define "text"}}` blocks, next to the existing incident URL line.

4. **Update fixtures and tests**:
   [emailpreview/fixtures.go:96](server/internal/handlers/emailpreview/fixtures.go:96)
   and the formatter tests
   ([formatter_test.go:29](server/internal/email/formatter_test.go:29)) pin the
   old slug-shaped URL; adjust them and add an assertion that the rendered
   "Check" table cell contains an `<a href>` wrapping the check name.

Verify with the email preview handler (`/dash0` email preview fixtures render
all templates) rather than by sending real mail.
