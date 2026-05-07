# Fix: manual incident resolve skips notifications

## Context

When an active incident resolves *automatically* — i.e. the next check result
comes back UP and the worker pipeline notices the state flip — the incident
service routes the lifecycle event through `Service.emitEvent`, which both
records the audit row in `events` *and* fans out a "resolved" notification
to every channel that fired on creation. Operators see a "resolved" message
in Slack/Discord/email; group-incident dedup is preserved; escalation steps
are canceled.

When an operator *manually* resolves the same incident — through the
dashboard "Resolve" button (`POST /api/v1/orgs/$org/incidents/$uid/resolve`)
or the equivalent CLI/MCP path — the service follows a separate code path
that creates the event row directly via `s.db.CreateEvent` and skips
`emitEvent` entirely. Channels never hear about the manual closure, even
though they were the ones paged for the open. From an on-call's perspective:
they got woken up, somebody else fixed the issue and clicked Resolve, and
their Slack channel still shows the incident as live. They learn it's
resolved when they next happen to refresh the dashboard.

## Reproduction

1. Wire a Slack channel and bind it to a check.
2. Force the check to fail (e.g. point an HTTP check at a 500-returning URL).
3. Wait for the incident to open — Slack receives the "incident opened" message.
4. Fix the URL so the check would succeed, but *before* the next check
   result arrives, click **Resolve** in the dashboard.
5. Slack stays silent. The dashboard shows the incident as resolved; Slack
   does not.

## Root cause

Two divergent code paths in `server/internal/handlers/incidents/service.go`:

- **Auto-resolve** at `resolveIncident` (~line 262) calls
  `s.emitEvent(ctx, …, EventTypeIncidentResolved, incident, …)` (line 281).
  `emitEvent` (line 873) creates the event row, then routes through
  `queueGroupNotifications` (group incidents) or `queueNotifications`
  (single-check incidents) for the resolved fan-out.

- **Manual resolve** at `resolveIncidentByOrgUID` (~line 1912) hand-builds
  an `Event` with `models.NewEvent(...)` (line 1949), persists it via
  `s.db.CreateEvent` (line 1962), then calls
  `s.cancelPendingNotifications` (line 1967). It never invokes
  `emitEvent`, so the dispatcher never runs.

Two side effects fall out of this:

1. **Channels miss the resolution event.** The on-call subscriber experience
   above.
2. **Group-incident events use the wrong CheckUID.** The manual path sets
   `event.CheckUID = &incident.CheckUID` (line 1951), but for a group
   incident `incident.CheckUID` is just the *primary* member — not a fan-out
   over all currently-failing members. Audit logs end up tagged with one
   member's UID even when the group spans many.

## Approach

Funnel the manual resolve through `emitEvent` so it shares the dispatcher
path with auto-resolve. The actor metadata (user UID, "via" reason, note,
manual resolution type) needs to ride along in the event payload, which
`emitEvent` already supports — the handler just hasn't been using it.

Concretely: replace the hand-built `Event` block in `resolveIncidentByOrgUID`
with an `s.emitEvent(ctx, orgUID, EventTypeIncidentResolved, incident,
payload)` call where the payload includes `via`, `note`, `resolution_type`,
`actor_uid`, and `check_uid`. Then teach `emitEvent` (or a small helper) to
honor `models.ActorTypeUser` when the payload carries an `actor_uid` so the
recorded event's `ActorType` is accurate (today `emitEvent` always writes
`ActorTypeSystem` at line 876).

The `cancelPendingNotifications` call at line 1967 stays where it is —
it's correct that we cancel any *pending* opens-side jobs even though we're
fanning a resolved event.

## Why not just add a parallel "manual resolve fan-out" call?

Two paths means two places to keep the suppression rules, group dedup,
escalation cancellation, and event payload schema in sync. The bug is
exactly the symptom of that drift — the manual path was added without a
back-pointer to the auto path's notification step. Routing both through
`emitEvent` makes the contract single-sourced.

## Files to edit

### `server/internal/handlers/incidents/service.go`

- `emitEvent` (line 873): take an optional actor (or read it off the
  payload's `actor_uid`) instead of hardcoding `ActorTypeSystem`. When
  `actor_uid` is present and non-empty, set `event.ActorType =
  ActorTypeUser` and `event.ActorUID = &actorUID`.
- `resolveIncidentByOrgUID` (line 1912): drop the manual event creation
  block (lines 1949–1965); replace with a single
  `s.emitEvent(ctx, orgUID, models.EventTypeIncidentResolved, incident,
  models.JSONMap{...})` call. The payload must include:
  - `via`: `req.Via`
  - `note`: `req.Note`
  - `resolution_type`: `models.ResolutionTypeManual`
  - `check_uid`: `incident.CheckUID` (kept for single-check dispatcher; group
    path ignores it)
  - `actor_uid`: `req.ActorUID` (when non-empty)
- Keep the `s.cancelPendingNotifications(ctx, incident.UID, nil)` call.

### `server/internal/handlers/incidents/service_test.go`

Add tests that pin the new contract:

1. `TestManualResolveQueuesNotifications` — fake `jobsSvc.Enqueue` and
   verify a `notification` job is created for each bound channel after
   manual resolve. Mirrors the existing auto-resolve test if one exists;
   if not, write the auto-resolve test in the same change so we have both
   sides covered.
2. `TestManualResolveGroupIncidentFansOutToAllMembers` — given a group
   incident with three members across two channels, manual resolve must
   queue one notification per (channel, "resolved") pair, deduped exactly
   like auto-resolve does today.
3. `TestManualResolveEventActorTypeIsUser` — when `req.ActorUID` is set,
   the resulting event row must have `actorType = "user"` and
   `actorUid = req.ActorUID`. (Today's hand-built path already does this;
   the regression risk is in the routed-through-`emitEvent` rewrite.)
4. `TestManualResolveAlreadyResolvedNoEvent` — calling resolve on an
   already-resolved incident is a no-op (today's behavior, line 1924–1926).
   Don't accidentally fan out a duplicate resolved on the second call.
5. `TestManualResolveSuppressedRolledUpChildStillEmitsResolved` — pin the
   asymmetry already encoded in `emitEvent` lines 888–892: a paging-suppressed
   child incident's *resolved* event still goes out (so timeline observers
   see closure) even though its *opened* event was suppressed.

## Verification

1. `make test` passes (existing tests + new ones above).
2. Manual smoke against `make dev-test`:
   - Login as `test@test.com`/`test`, create a Slack/webhook channel, bind
     to a check, force-fail, observe the open notification, then click
     "Resolve" and observe the resolution notification on the same channel.
3. Audit `events` table row for the manual resolve: `actor_type='user'`,
   `actor_uid` populated, `payload.via` matches the request body.

## Out of scope

- Adding a "resolution reason" picker to the dashboard. Today the
  resolution payload carries `note` and `via`, and the dashboard can
  already pre-fill them. UI changes are a separate spec.
- Reopening on a subsequent failed result (already handled — manual
  resolutions never auto-reopen per the comment at line 1898–1900).
- Notifications for `acknowledged` / `unacknowledged` / `snoozed`. Those
  are intentionally silent today (lines 917–918) and changing that
  carries a separate operator-impact discussion.

## Implementation Plan

Concrete commit breakdown:

1. **Teach `emitEvent` to honor a payload-carried actor.** Read
   `actor_uid` from the payload; when present and non-empty, set
   `event.ActorType = ActorTypeUser` and `event.ActorUID = &actorUid`.
   Otherwise keep today's `ActorTypeSystem`. No callers need to change
   for this commit alone — it's a pure capability addition.

2. **Route manual resolve through `emitEvent`.** Replace the hand-built
   event block in `resolveIncidentByOrgUID` with a single
   `emitEvent` call carrying `via`, `note`, `resolution_type`,
   `check_uid`, `actor_uid` in the payload. Drop now-unused imports
   if any.

3. **Test: manual resolve queues a notification job.** Single-check
   incident, one bound channel, manual resolve, assert one
   `notification` job was created.

4. **Test: manual resolve fans group incidents.** Group incident with
   multiple members across overlapping channels, manual resolve,
   assert one notification per (channel, eventType) pair.

5. **Test: actor metadata round-trips.** Manual resolve with
   `req.ActorUID` set, assert the persisted event row has
   `ActorType=user` and `ActorUID` matches the request.

6. **Test: idempotent resolve.** Calling resolve on an already-resolved
   incident returns early without emitting a duplicate event.

7. **QA**: `make build-backend lint-back test`. Iterate until clean.

8. **Audit**: independent subagent verifies every requirement.

9. **Archive + merge**: move spec to `specs/done/2026/05/`, merge
   `feat/fix-manual-incident-resolve-missing-notifications` into `main`.
