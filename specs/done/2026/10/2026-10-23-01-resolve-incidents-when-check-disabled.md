---
model: sonnet
effort: medium
---

# Disabling a check leaves its open incidents open forever

GitHub Issue: https://github.com/fclairamb/solidping/issues/647

## Source

Issue #647 (filed 2026-10-23): when a check with an open incident is disabled by hand, the
incident stays open and keeps showing as ongoing on the dashboard, so the open-incident count
stops meaning anything. The reporter suggests offering to resolve the open incidents in the
same action: a confirmation in the UI ("This check has 1 open incident. Resolve it?", default
yes) and the same option on the API (`PATCH` with `enabled: false` plus
`resolveOpenIncidents: true`). The resolution must say the incident was closed because the
check was disabled, not because it recovered. Workaround: resolve each incident by hand.

The report is right. Nothing closes the incident once the check stops running.

## Problem

- `UpdateCheck` (`server/internal/handlers/checks/service.go:2282`) copies `req.Enabled`
  into the update, writes it, then only calls `resolveDegradedOnDisable`
  (`service.go:2403`), which reacts to `degradedEnabled: false`, not to `enabled: false`.
  `enabled: false` only feeds the job reconcile (`service.go:2410`), which removes the
  check's jobs.
- With no jobs, no result ever arrives, so the adaptive auto-resolution in the incidents
  service never runs for that check. An open outage or degraded incident stays
  `state = active` indefinitely.
- Two precedents already close incidents on a lifecycle event, and give the shape to copy:
  - `ResolveDegradedOnDisable` (`server/internal/handlers/incidents/degraded.go:299`) closes
    the open degraded incident with `resolution_type = "disabled"` when degraded detection
    is turned off, through the `DegradedIncidentResolver` hook wired into the checks service
    (`server/internal/handlers/checks/degraded.go:144-175`).
  - Check deletion resolves every active incident with `resolution_type = "check_deleted"`
    (`models.ResolutionTypeCheckDeleted`, `server/internal/db/models/incident.go`), then
    `NotifyCheckDeletedIncidents` (`server/internal/handlers/incidents/check_deleted.go`)
    cancels pending escalation steps and emits one `incident.resolved` event per incident.
- The manual resolve path (`ResolveIncident`, `incidents/service.go:4009`) uses
  `resolution_type = "manual"` and records `resolved_by`. Using it here would lose the
  "closed because disabled" meaning the reporter asks for.
- The dashboard toggle (`handleToggleEnabled`,
  `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1147`) sends
  `{ enabled: next }` with no confirmation.

## Why a spec

- Needs a changed API endpoint: a new `resolveOpenIncidents` field on the check update
  request, in `openapi.yaml` (`make generate`), and likely on `/apply`, import and the MCP
  `update_check` tool, which all funnel through `UpdateCheck`.
- Needs a product decision the reporter did not settle: opt-in prompt versus always closing,
  and which `resolution_type` to record (see Open questions).

## Proposal

### Backend

1. Add `ResolveOpenIncidents *bool` (`resolveOpenIncidents`) to `UpdateCheckRequest`
   (`checks/service.go:1991`) and to the OpenAPI schema of the check `PATCH`. Ignored unless
   the same request sets `enabled: false`. A request with `resolveOpenIncidents: true` and no
   `enabled: false` is a `VALIDATION_ERROR`.
2. Add a new resolution type `models.ResolutionTypeCheckDisabled = "check_disabled"` (see
   Open question 2). No migration is needed: `resolution_type` is a free text column (verify
   there is no CHECK constraint on either dialect; if there is one, this becomes a migration
   in the next `NNN` file).
3. Add `ResolveOnCheckDisable(ctx, orgUID, checkUID string) ([]string, error)` to the
   incidents service: resolve every active incident (outage and degraded) of the check with
   `resolution_type = "check_disabled"`, `resolved_by` = the caller when known, cancel
   pending escalation steps before emitting, emit one `incident.resolved` per incident with
   `resolutionType` in the payload, and publish the live update. Reuse the
   `NotifyCheckDeletedIncidents` sequence rather than duplicating it.
4. Wire it into `UpdateCheck` next to `resolveDegradedOnDisable`, through a small interface
   on the checks service (same pattern as `DegradedIncidentResolver`), called only when
   `enabled` is `false` and `resolveOpenIncidents` is `true`. Like the degraded hook, fire on
   any write that sets both, not only on a true to false transition, so a retry after a
   failed resolve still closes them.
5. Expose the open-incident count the UI needs: `GET` check already returns the check; add
   `openIncidentCount` to the check response, or let the UI query
   `GET /incidents?checkUid=<uid>&state=active&limit=1` (pick the one that needs no new
   field if it gives the count).

### Notifications and UI copy

6. Resolved notifications (Slack, email, webhook, Telegram) read `resolutionType`: add the
   `check_disabled` wording ("resolved: check disabled") wherever `check_deleted` has its own
   wording today (`server/internal/notifications/check_deleted.go`, `webhook.go`,
   `job_incident_resolution_notice.go`).
7. dash0: add `"check_disabled"` to the `resolutionType` union (`web/dash0/src/api/hooks.ts:748`)
   and a label on the incident page next to the `check_deleted` one
   (`incidents.$incidentUid.tsx:1224`).
8. dash0: when the user clicks Disable on a check with at least one active incident, open a
   confirmation dialog: "This check has N open incident(s). Resolve them?", with a checkbox
   checked by default, and send `{ enabled: false, resolveOpenIncidents: <checkbox> }`. With
   no open incident, keep today's one-click toggle. `data-testid` on the dialog, checkbox and
   buttons. i18n keys in every locale the checks namespace ships.

### Docs and tests

9. Docs: the check `PATCH` reference (generated) and the incidents page in `web/docs/` (how
   incidents end, the new resolution type). CHANGELOG entry.
10. Go tests (SQLite, `make test`): disabling with `resolveOpenIncidents: true` resolves an
    open outage and an open degraded incident with `check_disabled`; disabling without it
    leaves them active (positive control for the absence); `resolveOpenIncidents: true`
    without `enabled: false` is rejected; the resolved event is emitted once per incident.
11. Playwright: disable a check with an open incident, keep the checkbox, see the incident
    resolved with the "check disabled" label.

## Open questions

1. **Opt-in or automatic?** Recommended: opt-in on the API (`resolveOpenIncidents`, default
   false) and default-checked in the UI, as the reporter suggests. Trade-off: an API or
   `/apply` user who disables a check still gets the stale incident unless they pass the flag;
   always closing would be simpler but silently changes the meaning of `enabled: false` for
   every existing script, and some users disable a check to stop the noise while the outage
   is still being worked on.
2. **Which resolution type?** Recommended: a new `check_disabled`. Trade-off: `disabled`
   already exists but means "degraded detection turned off" (`incident.go`), and reusing it
   would make the history ambiguous; a new value means one more case in every notification
   template and in the dash0 union.

## Closing the issue

This spec closes #647. The implementing PR body **must** carry one `Closes #647` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 647 --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. Opt-in on the API (`resolveOpenIncidents`, default false), default-checked in the UI. (autopilot: recommended answer)
2. New resolution type `check_disabled`. (autopilot: recommended answer)
