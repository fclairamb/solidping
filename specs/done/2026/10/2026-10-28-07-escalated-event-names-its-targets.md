---
model: sonnet
effort: medium
---

# "Incident escalated" events do not say who or what was paged

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/29

## Source

In-app report from the Events page, on a phone (v0.42.0): five "Incident
escalated" rows in a row, each with only a time. The reporter wants the
integration the incident was escalated to, by name and linked: "there is a link,
but the name is never given, which makes the audit impractical".

The report is right. The link it mentions is the Related column (a generic
"Check" / "Incident" chip, off-screen on a phone); nothing names the paged
integration anywhere on the row.

## Problem

- The escalation-policy path records `incident.escalated` in
  `emitEscalatedEvent` (`server/internal/jobs/jobtypes/job_escalation_step.go:1677`)
  with counts only: `step_uid`, `step_position`, `repeat_index`, `target_count`,
  `notification_jobs`, `direct_emails`, `skipped`. No policy name, no check name,
  and no name or uid of the integrations, users or schedules paged.
  `fanOutWithSeverity` (`job_escalation_step.go:212`) knows each target as it
  pages it but returns only `fanOutStats`.
- Because the payload has no `check_name`, the Related chip of these rows falls
  back to the generic "Check" label (`getEventCheckName`,
  `web/dash0/src/components/dashboard/event-display.tsx`). The other emitter, the
  threshold escalation in `server/internal/handlers/incidents/service.go:1381`,
  does record `check_name`.
- `getEventDetail` (`web/dash0/src/components/dashboard/event-log-table.tsx`) has
  no case for `incident.escalated`, so the row's second line stays empty.

## Why a spec

- The event payload gains new fields that the webhook and API consumers also see,
  and the fan-out has to collect target names across four target kinds
  (integration, user, schedule, all admins).
- Size: the escalation job, its tests and the frontend row exceed the direct-fix
  bound.

## Proposal

1. **Record what was paged.** Have `fanOutWithSeverity` return, next to the
   counts, the list of paged targets: for an integration `{type: "integration",
   uid, name}`, for a user `{type: "user", uid, name}`, for a schedule `{type:
   "schedule", uid, name}` (plus the on-call user it resolved to, when known), for
   all admins `{type: "all_admins"}`. Skipped targets are not listed (the
   `skipped` count stays). Add to the payload: `targets` (that list, capped at
   20 entries), `policy_uid`, `policy_name`, `check_name`, `check_slug`.
2. **Show it.** In `getEventDetail`, for `incident.escalated`: "Step 2 · Ops
   Slack, alice" with each integration linked to
   `/orgs/$org/integrations/$integrationUid` and each schedule to its on-call
   page. Fall back to the policy name, then to nothing, for rows recorded before
   the change. Same on the incident timeline if it renders these events
   separately.
3. Names are captured at fire time, like `check_name`: a renamed integration keeps
   its old name on old rows, which is the right record for an audit.
4. Tests: a job test asserting the payload for a step with one integration and one
   user target, and one with a severity filter that skips the integration; a
   vitest case for the detail line, including an old row without `targets`.
5. Docs: list the new payload fields where `incident.escalated` is documented
   (`web/docs/docs/features/events.md`, webhook payload docs if they list it).

## Closing the issue

This spec closes fclairamb/solidping-business#29. The implementing PR body
**must** carry `Closes fclairamb/solidping-business#29`. When the spec is archived
to `specs/done/`, verify the issue is closed and, if the merge did not close it,
close it by hand:

    gh issue close 29 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
