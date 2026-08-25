---
model: sonnet
effort: low
---

# The escalation Slack DM addresses the incident by full UID, with no name and no link

## Problem

The per-user escalation Slack DM still sends a generic, UID-based message
(`server/internal/jobs/jobtypes/job_escalation_step.go:583`, in
`sendEscalationSlackDM`):

```
[escalation] incident %s requires your attention. Open the dashboard to acknowledge or resolve.
```

with `incident.UID` interpolated. Three problems, the same ones just fixed for
the browser web-push path in the same file:

- **Full incident UID instead of the short `#N` reference.** `incident.Number`
  exists precisely so no human-facing surface shows a 36-char UUID (see the
  convention comment on `models.Incident.Number`); Slack headers elsewhere use
  `#42 · ` (`notifications.incidentRefPrefix`).
- **No check name.** The recipient can't tell what is down without opening the
  dashboard.
- **No link.** "Open the dashboard" without a URL — the DM should deep-link to
  the incident detail page.

## Proposal

Rework `sendEscalationSlackDM` the way `sendWebPush` in the same file was
reworked (commit `fix(notifications): make browser push notifications
incident-aware` on `batch/2026-08-21`):

1. **Extract a pure message builder** (mirroring `escalationWebPushMessage`)
   that takes the incident, check name, org slug, and base URL, and returns the
   DM text:
   - Address the incident as `Incident #42 for <check name>` — omit the `#N`
     part when `incident.Number <= 0`, and **never** fall back to the UID in
     visible text.
   - Resolve the check name via `jctx.DBService.GetCheck` +
     `escalationCheckName` (falls back to "Unknown check"); do **not** use
     `incidentCheckName`, whose fallback is the UID.
   - Build the incident URL with the existing helpers: `appBaseURL(jctx)`,
     `orgSlugForOrg(...)` for the slug, and `escalationIncidentURL(baseURL,
     orgSlug, incident)`. Use Slack's `<url|text>` hyperlink syntax when the
     URL is non-empty; degrade to plain text (no link) when base URL or slug
     is unavailable, as the phone/SMS path already does.
2. **Keep delivery untouched** — same `postSlackDM` call, same
   severity gate (`severityAllowsEmail`), same error handling.
3. **Add a unit test** for the message builder (table-driven,
   `testify/require`, `t.Parallel()`): numbered incident with link, unnumbered
   incident (no `#N`, name still present), missing base URL / org slug (no
   link), and assert the UID never appears in the text — mirroring
   `TestEscalationWebPushMessage` in
   `job_escalation_step_webpush_test.go`.
