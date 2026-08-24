---
model: sonnet
effort: medium
---

# Invitations should be email-first: send the email visibly, not just show a link

## Problem

On `/dash0/orgs/$org/organization/invitations`, creating an invitation appears to
only produce a shareable link. The success state of the dialog
(`web/dash0/src/components/shared/create-invitation-dialog.tsx:107-134`, string
`invitationCreated` in `web/dash0/src/locales/en/org.json:169`) says
"Invitation created. Share this link:" and shows a copy field — nothing else.

The reality is more subtle, and worse in some ways:

1. **An email is already sent** — `CreateInvitation` calls `sendInvitationEmail`
   (`server/internal/handlers/auth/service.go:3180`, template
   `server/internal/email/templates/invitation.html`) — but the UI gives zero
   acknowledgement, so the inviter believes they must copy/paste the link
   manually, and the recipient may get a surprise duplicate.
2. **Sending can silently do nothing.** `enqueueEmail` failures are only logged
   (`service.go:524-548`), and the SMTP sender's `Send` is a no-op when
   `SP_EMAIL_ENABLED=false` (`server/internal/email/sender.go:57-63`). The
   inviter can never tell whether an email actually went out.
3. **The rendering lies.** `invitation.html:9,20` hardcodes "This invitation
   expires in 7 days", while the actual TTL defaults to 24h and ranges 1h–1w
   (`service.go:3059`, `getAllowedInviteExpirations`).

So the user-facing fix is not "start sending an email" — it's "make the email
the visible primary channel, truthful in content, with the link as fallback".

## Proposal

### Backend

- Have `POST /api/v1/orgs/:org/invitations` report the email outcome in its
  response, e.g. `"email": {"sent": true}` — `sent: false` when email is
  disabled on the instance (`SP_EMAIL_ENABLED=false` / org-level override) or
  when the enqueue failed. Since delivery is async (job type `email`,
  `server/internal/jobs/jobtypes/job_email.go`), "sent" means "queued for
  delivery"; keep the naming honest (`queued` is fine too — pick one and use it
  in the OpenAPI spec).
- Fix `invitation.html` to render the **actual** expiry: pass the human-readable
  `expiresIn` into the template data and replace the hardcoded "7 days" in both
  the HTML body and any text part. Update the emailpreview fixture
  (`server/internal/handlers/emailpreview/fixtures.go`) so the preview harness
  shows the dynamic value.

### Frontend (dash0)

- Rework the dialog success state to lead with the email: "An invitation email
  was sent to alice@acme.com." The link stays available as a secondary
  fallback ("Or share this link directly:") with the existing copy button.
- When the response says the email was **not** sent (email disabled / enqueue
  failed), keep the link as the primary path and say so explicitly ("Email
  sending is not configured on this instance — share this link with the
  recipient."), so the inviter knows the link is the only channel.
- All new strings go through the locale files (`org.json`) in every supported
  locale.

### Testing

- Backend: table-driven test that the create response reports `sent: true` when
  email is enabled and `sent: false` when disabled; template test/golden or
  emailpreview check that the rendered email shows the real expiry (e.g. create
  with `expiresIn: "1h"` → email says 1 hour, not 7 days).
- E2E: extend `web/dash0/e2e/invitations.spec.ts` to assert the new success copy
  (test mode runs with email disabled, so it should exercise the fallback copy
  path — assert that branch explicitly).

## Out of scope (candidates for follow-up specs)

- A resend endpoint and per-invitation delivery status (bounce visibility via
  the existing `emailsuppressions` table) — today a bounced invite is invisible
  to the admin.
- Letting dash0 set `expiresIn` and offering the `viewer` role in the dialog
  (both already supported by the backend, absent from the UI).
