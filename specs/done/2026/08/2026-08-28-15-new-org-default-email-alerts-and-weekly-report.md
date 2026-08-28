---
model: sonnet
effort: medium
---

# A brand-new org receives no incident emails and no weekly report until its owner hand-builds both

## Problem

The product pitch is "you get notified of incidents by email, by default, and a
weekly report on top" — but a freshly created org that does *nothing* receives
**zero** email, ever:

- Incident fan-out only reads the channels attached to the failing check
  (`queueNotifications`, `server/internal/handlers/incidents/service.go:1572-1590`
  → `ListChannelsForCheck`). No integration is ever seeded: `CreateOrg`
  (`server/internal/handlers/auth/service.go:2995`) creates the org, the owner
  membership, an activation event and a session — nothing else.
- The mechanic that would make email work out of the box already exists and
  sits unused: an integration flagged `isDefault`
  (`server/internal/db/models/integration.go:109`) is auto-attached to every
  **newly created** check (`server/internal/handlers/checks/service.go:1444-1450`).
  Nothing ever creates a default integration, so the auto-attach never fires.
- Report schedules (weekly/monthly uptime digest, timezone-aware, with
  unsubscribe — `server/internal/uptimereport/`) exist but are strictly
  opt-in: `models.NewReportSchedule` is only called from the create endpoint
  (`server/internal/handlers/reportschedules/service.go:185`), buried under
  organization settings.

The personal-paging path does lazily seed an email route from the account
email (`EnsureDefaultEmailRoute`,
`server/internal/handlers/usernotifications/service.go:196-206`), but it only
delivers through escalation policies, which a new user has not built either.

Net effect: the "default" experience is silence, and the fix belongs in
seeding, not in asking the user to click through an onboarding step.

## Proposal

When a user self-creates an org, seed two rows so alerting and the weekly
digest work from minute one. Both are ordinary, visible, editable resources —
the user can rename, retarget or delete them like anything hand-made.

1. **Default email integration.** In `CreateOrg`
   (`server/internal/handlers/auth/service.go:2995`), after the owner
   membership is created:
   - `models.NewIntegration(org.UID, <email type>, "Email alerts")`
     (`integration.go:125`), with `Settings["to"] = [ownerEmail]` — `to` is
     the canonical recipients key the email notifier reads
     (`server/internal/notifications/email.go:296-299`) — `IsDefault: true`,
     enabled.
   - The owner's email comes from the authenticated user row (`CreateOrg`
     already has `userUID`; fetch the user via `s.db`). Never log the address
     — same PII bar as `ReportSchedule.Recipients`
     (`report_schedule.go:36-38`).
   - Because the org is brand new, every check it will ever have is created
     *after* this integration exists, so the existing `isDefault` auto-attach
     (`checks/service.go:1444-1450`) gives full coverage with no retro-attach
     logic.

2. **Default weekly report schedule.** Same place:
   `models.NewReportSchedule(org.UID, "Weekly uptime report",
   models.ReportFrequencyWeekly)` (`report_schedule.go:62` — the constructor
   already defaults `Enabled: true`, `IncludeSLOs: true`, timezone UTC), with
   `Recipients = [ownerEmail]` and **both scope slices left empty** — empty
   scope means org-wide (`report_schedule.go:40,57`), so checks added later
   are covered automatically. The hourly `JobTypeUptimeReport` job picks it up
   with no further wiring, and the digest already carries an unsubscribe link
   (`server/internal/uptimereport/unsubscribe.go`), which is what makes
   default-on defensible.

3. **Failure posture: best-effort.** A failed seed must never fail org
   creation — log a WARN and continue. A signup that 500s because a
   convenience row could not be written is strictly worse than the current
   behavior.

4. **Where NOT to seed** — explicitly out of scope:
   - The bootstrap default org (`ensureDefaultOrganization`,
     `server/internal/jobs/jobtypes/job_startup.go:206-283`). Its owner is the
     placeholder `admin@solidping.io` — solidping.io is a real domain, and
     thousands of self-hosted instances mailing it once they configure SMTP is
     a leak, not a feature.
   - The test-mode org (`server/test/testdata/testdata.go`) — Playwright
     suites depend on its fixtures being exactly what they build.
   - **Existing orgs.** No migration, no retro-seeding: surprising an
     established org with new outbound email is worse than the gap.

5. **SMTP-unconfigured is fine.** Seeding is not gated on SMTP being
   configured: the rows are inert until the server can send (SaaS always can;
   self-hosted starts working the moment the admin configures mail). Surfacing
   the "SMTP not configured" state to the user is the onboarding checklist's
   job (spec 2026-08-28-17), not this one's.

6. **Tests** (table-driven, `testify/require`):
   - `CreateOrg` seeds exactly one enabled `isDefault` email integration with
     the owner's address under `to`, and one enabled org-wide weekly schedule
     with the owner as recipient.
   - A check created in the new org comes out with the seeded integration
     attached (proves the auto-attach handshake end to end).
   - The bootstrap default org and the test-mode org get **no** seeded
     integration/schedule (pin the exclusions).
   - A seed failure (e.g. forced DB error) still returns a successful
     `OrgResponse`.

Open question for the implementer: whether to insert via `s.db` model writes
(as `CreateOrg` does for the org and membership) or route through the
integrations/reportschedules services for audit-event parity — prefer whichever
keeps the seeded rows indistinguishable from hand-created ones in the events
feed, but do not add a service→service dependency cycle for it.
