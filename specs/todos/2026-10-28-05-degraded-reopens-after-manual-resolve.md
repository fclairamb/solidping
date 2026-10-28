---
model: opus
effort: medium
---

# A manually resolved degraded incident reopens on the next sweep

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/26

## Source

Seen in a session replay on a new org's first day. A browser check had 5 failed
runs during its setup (wrong regions and selector, fixed within minutes). It then
opened a **degraded** incident ("5 failures in the last N probes"). The user
resolved it by hand four times in about 35 minutes; each time a new degraded
incident opened shortly after (the last one 51 s after the resolve), and each one
sent an incident email. The user ended up disabling the check. N grew from 7 to
22 to 36 probes while the check was Up and passing.

The reporter's reading ("the window is since creation") is close but not exact:
the window is a count of 60 probes, which a check created an hour earlier has not
filled yet, so every setup failure is still inside it. The real defect is that a
manual resolve does not move the window.

Same session, smaller things on the check page:
- The Status card shows a bare duration (`16s`) while the check is
  **Validating**: no label says what the duration measures.
- The availability table says "Since creation · 1d monitored" for a check created
  about 1 h earlier, and "Incidents 0" while 4 degraded incidents had opened.

## Problem

- The degraded evaluator (`server/internal/handlers/degradedeval/service.go:104`)
  reads probes since `probesSince` (`service.go:224-240`): 24 h back, or the last
  resolve of a **check** incident if later. `FindRecentlyResolvedIncidentByCheckUID`
  filters `kind = 'check'` (`server/internal/db/sqlite/sqlite.go:3893`, same in
  postgres), so the resolve of a **degraded** incident, manual or automatic, never
  moves `since`.
- The failure rule is "5 failures in the last 60 probes"
  (`models.DefaultDegradedFailures` / `DefaultDegradedFailuresWindow`,
  `server/internal/db/models/check.go:625-626`), each probe kept up to
  2 x N x period (`server/internal/degraded/rule.go:314`). The 5 setup failures stay
  countable for up to 60 probes, so after a manual resolve the next sweep finds
  the rule still firing, sees no open degraded incident (`applyFiring`,
  `service.go:183`) and opens a new one, which notifies again.
- `N` in the title is `FailureSlots`, the countable probes actually found
  (`degradedeval/service.go:371`), which grows until the window is full: hence
  7, 22, 36.
- Status card: `check-summary-cards.tsx:56-62` has labels for up, down and stale
  only; validating falls through to the generic "Status" fallback.
- Availability: `availability-rows.ts:120` rounds `monitoredSeconds` to days with
  a floor of 1 (`Math.max(1, …)`), so 1 h reads "1d monitored". The incident count
  of the rows does not include degraded incidents.

## Why a spec

- Correctness of the incident lifecycle (when an incident may reopen, which
  notifications go out), across the evaluator and both DB dialects.
- A product decision the reporter did not settle (see Open questions).
- Three UI fixes on top: over the direct-fix size.

## Proposal

1. **A resolve resets the degraded window.** In `probesSince`, also take the last
   resolved **degraded** incident of the check (new query, or extend the existing
   one with a kind parameter, sqlite + postgres): probes before its `resolved_at`
   are not counted. This covers the manual resolve and keeps the auto-resolve
   consistent (a fresh incident needs fresh failures).
2. **No reopen notification storm.** With 1, a reopen needs M new failures after
   the resolve, so the four-emails case disappears. Add a regression test in
   `degradedeval/service_test.go`: 5 failures, open, manual resolve, then passing
   probes: no new incident; then 5 new failures: a new incident.
3. **Status card while validating**: add a "Validating for" label
   (`detail.summary.validatingFor`, 4 locales) in `check-summary-cards.tsx`.
4. **Availability for a young check**: show hours below one day ("1h monitored")
   instead of rounding up to `1d`, and count degraded incidents in the Incidents
   column, or label the column so it is clear it counts outages only (see Open
   questions).
5. Docs: the degraded section of `web/docs/docs/features/incidents.md` says that a
   resolve (manual or automatic) restarts the count.

## Open questions

1. **What does a manual resolve mean for degraded?** Recommended: probes before
   the resolve no longer count (proposal 1). Trade-off: a check that keeps
   failing intermittently needs M fresh failures to reopen, so it is reported
   later; the alternative (a bounded time window, for example the last hour) changes
   the rule's semantics for every check and its docs.
2. **Incidents column**: count degraded incidents too, or keep outages only and
   say so? Recommended: count outages only and rename the header "Outages" (or add
   a tooltip): availability is computed from outages, mixing in degraded
   incidents would contradict the percentage next to it.

## Closing the issue

This spec closes fclairamb/solidping-business#26. The implementing PR body
**must** carry `Closes fclairamb/solidping-business#26`. When the spec is archived
to `specs/done/`, verify the issue is closed and, if the merge did not close it,
close it by hand:

    gh issue close 26 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
