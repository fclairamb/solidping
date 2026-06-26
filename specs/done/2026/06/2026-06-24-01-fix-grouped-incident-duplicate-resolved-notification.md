# Fix: grouped / rolled-up incident sends a duplicate "resolved" notification

## Context

SolidPing has two "incident grouping" mechanisms whose whole purpose is to
turn a correlated multi-check outage into **one** page and **one** resolution:

- **Group incidents** (`2026-04-29-04-group-incident-correlation.md`) — checks
  that share a `check_group_uid` collapse into a single group incident with a
  per-member timeline. Notifications fan out once per connection
  (`queueGroupNotifications`).
- **Cascade rollup** (`2026-05-03-57-check-dependencies-and-cascade-rollup.md`)
  — when a hard dependency (parent check) is already down, a downstream
  (child) check's incident is *rolled up*: it is tagged
  `caused_by_incident_uid = <parent>` and `paging_suppressed = true`, and its
  **opened** / **escalated** notifications are skipped. Paging fires only on
  the root-cause incident.

The promise of both is: **one correlated outage ⇒ one "opened" page ⇒ one
"resolved" message** on the channels that were paged.

That promise is broken on resolution. The reporting Slack thread (channel
`C0A65GHTC3Z`, screenshot attached to the request) shows a single check
("Claude API", `GET https://status.claude.com/api/v2/status.json`) recovering
and the channel receiving the **"✅ Incident resolved after 5 hours 24
minutes."** message **twice**:

1. once as a **bare top-level message** with no monitor / cause / link
   context, and
2. once as a **thread reply** nested under the original incident message.

Both carry the same `5h24m` duration — consistent with two incidents that
started within the same correlation window (a parent and its rolled-up child),
not two unrelated incidents.

Reported in Slack:
- https://solidping.slack.com/archives/C0A65GHTC3Z/p1782249099331199
- https://solidping.slack.com/archives/C0A65GHTC3Z/p1782250119447319

## Symptom

On recovery of a correlated outage, paged channels receive **N+1** "resolved"
messages instead of one: the root incident's proper threaded resolution **plus
one extra, context-free top-level "Incident resolved after X" per rolled-up
child** that recovered before (or at the same time as) the root. The extra
message has no monitor name, no cause, and no link, because it is not attached
to any prior incident message.

## Root cause (diagnosed from code; confirm with the repro below)

The rollup feature suppresses a child's **opened** notification but **not** its
**resolved** notification, and the Slack sender then turns that orphan resolved
event into a brand-new top-level message.

1. **`emitEvent` deliberately exempts `resolved` from paging suppression.**
   In `server/internal/handlers/incidents/service.go` (`emitEvent`, ≈line
   1137):

   ```go
   if incident.PagingSuppressed && eventType != models.EventTypeIncidentResolved {
       // Rolled-up child: notifications are deferred until parent
       // resolves. Resolve still notifies so timeline observers see closure.
       return nil
   }
   ```

   So a paging-suppressed child whose **opened** event was suppressed still
   reaches the fan-out (`queueNotifications` for a per-check incident, ≈line
   1155) on **resolve**. This asymmetry is intentional today — it is even
   pinned by `TestManualResolveSuppressedRolledUpChildStillEmitsResolved` in
   `2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md`. The
   intent ("timeline observers see closure") is reasonable, but it conflates
   *recording the lifecycle event* (for the dashboard timeline) with *paging a
   notification channel*.

2. **The child has no Slack thread state, so the resolved becomes a new
   top-level message.** Because the child's `opened` event returned early at
   step 1, the Slack sender never stored thread state under
   `incidents/<childUID>/slack/thread`. In
   `server/internal/notifications/slack.go` (`Send`, ≈line 47):

   ```go
   if payload.EventType == eventTypeIncidentResolved && threadEntry != nil && threadEntry.Value != nil {
       return s.handleIncidentResolution(...) // update original + ONE thread reply
   }
   ...
   return s.postNewMessage(...) // threadEntry == nil ⇒ posts a NEW top-level message
   ```

   With no thread entry, the resolved falls through to `postNewMessage`
   (≈line 91), whose body is `buildIncidentResolvedThreadReply` — the bare
   ":white_check_mark: Incident resolved after X." text — but posted
   **standalone at the top level** (no `ThreadTS`). That is the orphan
   message in the screenshot.

3. **The root/parent incident posts the correct threaded resolution.** Its own
   `resolved` runs `handleIncidentResolution`: it updates the original message
   in place and posts one thread reply. That is the second "resolved" message.

Net: root threaded resolution + one orphan top-level resolution per recovered
suppressed child = the duplicate.

Notes:
- The defect is **channel-agnostic at the source** (`emitEvent` fan-out), so
  Discord (`notifications/discord.go`), Google Chat
  (`notifications/googlechat.go`), webhook, and email also emit the duplicate
  resolution. Slack's "post a new top-level message when there is no thread
  state" merely makes the orphan especially confusing (no context).
- **Group incidents (`check_group_uid`) are not the trigger** — a group is a
  single incident and `queueGroupNotifications` dedupes by connection. The
  duplicate comes from the **cascade-rollup** path, where the child is a
  *separate* incident. The reproduction confirms which path is at fault.

## Reproduction

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# 1. Wire a Slack channel bound to BOTH checks (or the org default channel).
# 2. Create a parent check and a child check, both pointing at a dead target,
#    with a hard dependency parent -> child so the child rolls up under the parent.
PARENT=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"dep-parent","type":"http","config":{"url":"http://127.0.0.1:1"},"period":"5s","incidentThreshold":1}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq -r '.uid')
CHILD=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"dep-child","type":"http","config":{"url":"http://127.0.0.1:1"},"period":"5s","incidentThreshold":1}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq -r '.uid')
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"parentCheckUid\":\"$PARENT\",\"kind\":\"hard\"}" \
  "http://localhost:4000/api/v1/orgs/default/checks/dep-child/dependencies"

# 3. Wait for the parent incident to open and the child to roll up under it
#    (child incident: paging_suppressed=true, caused_by_incident_uid=parent).
#    Only ONE "incident opened" Slack message should appear (the parent).

# 4. Fix BOTH checks so they recover within the same window.
for slug in dep-parent dep-child; do
  curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"config":{"url":"https://example.com"}}' \
    "http://localhost:4000/api/v1/orgs/default/checks/$slug"
done

# 5. BUG: the channel receives TWO "Incident resolved after X" messages —
#    one threaded under the parent, one bare top-level from the child.
#    EXPECTED: exactly one resolved message (threaded under the parent).
```

## Approach

Restore the rollup invariant: a `paging_suppressed` incident must be **silent
on paging channels for its entire lifecycle** — opened, escalated, **and
resolved** — while still **recording** its lifecycle events for the dashboard
timeline. "Timeline observers see closure" should mean the `events` table /
dashboard, **not** a Slack/Discord/email page.

Concretely, in `emitEvent` decouple *recording the event* (already done via
`s.db.CreateEvent`) from *fanning out a notification*:

- Keep creating the `incident.resolved` event row unconditionally (the
  dashboard timeline still shows closure).
- Gate the **notification fan-out** (`queueNotifications` /
  `queueGroupNotifications` / escalation) on the **current**
  `incident.PagingSuppressed` value for **all** lifecycle event types,
  including `resolved`. I.e. drop the `&& eventType != EventTypeIncidentResolved`
  exception so a still-suppressed child does not page on resolve.

This is correct for the load-bearing rollup case because the
**child-still-down-when-parent-resolves** path already un-suppresses the child
first: `reEvaluateChild` (`server/internal/handlers/incidents/rollup.go` ≈line
173) flips `paging_suppressed = false` and emits `IncidentReopened`, which
pages then. By the time such a child later resolves, `paging_suppressed` is
already `false`, so it will correctly send its own resolution. Gating on the
*current* flag value therefore pages exactly the incidents that were ever
paged — no more, no less:

| Scenario | Opened page | Resolved page (after fix) |
|---|---|---|
| Root / standalone incident | yes | yes (threaded) |
| Child rolled up, recovers before/with parent | no | **no** (was: orphan top-level) |
| Child rolled up, still down when parent resolves | no, then un-suppressed → paged | yes (it became a real standalone incident) |

### Defense-in-depth (Slack sender)

Independently, make the Slack resolved path not invent a context-free
top-level message: in `SlackSender.Send`
(`server/internal/notifications/slack.go` ≈line 47), when the event is
`resolved` but there is **no stored thread state**, skip posting rather than
falling through to `postNewMessage`. There is no original message to update or
thread under, so a standalone bare "resolved" message is never the right
output. This bounds the blast radius of any future "resolved with no opened"
path and is cheap. (Apply the same guard to the `reopened` branch, which has
the identical `threadEntry`-required shape.)

## Files to edit

### `server/internal/handlers/incidents/service.go`
- `emitEvent` (≈line 1133–1155): remove the resolved-event exception from the
  paging-suppression early-return so a still-suppressed incident records its
  event but does **not** fan out notifications/escalation on resolve. Make sure
  the event row is still created in all cases (it is created before the switch,
  so this is just about the fan-out gate). Re-read the comment at ≈1138–1139
  and update it to reflect "record event, do not page."

### `server/internal/notifications/slack.go`
- `Send` (≈line 47–56): when `EventType == resolved` (and `reopened`) and
  `threadEntry`/thread state is absent, return without posting instead of
  calling `postNewMessage`.

### Tests
- **Update** `TestManualResolveSuppressedRolledUpChildStillEmitsResolved`
  (added by `2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md`)
  — its expectation is now the bug. The new contract: a still-suppressed
  child's resolve **creates the event row** but **enqueues no notification
  job**.
- **Add** `TestRolledUpChildResolveDoesNotPageWhenSuppressed` — child with
  `paging_suppressed=true` resolves ⇒ exactly zero notification jobs, one
  `incident.resolved` event row.
- **Add** `TestUnsuppressedChildResolveStillPages` — child that was
  un-suppressed by parent resolution (`reEvaluateChild`) and later resolves ⇒
  one notification job per bound channel (regression guard so the fix doesn't
  silence real standalone resolutions).
- **Add** a Slack-sender test: `resolved` payload with no stored thread state ⇒
  no `PostMessage` call (no top-level orphan).
- Keep all existing per-check and group-incident resolution tests green.

## Acceptance criteria

- [ ] In the reproduction above, recovery produces **exactly one** "Incident
      resolved after X" message on the channel — threaded under the original
      (root) incident message — and **no** bare top-level resolved message.
- [ ] A rolled-up child whose `opened` page was suppressed sends **no**
      `resolved` page on any channel (Slack, Discord, Google Chat, webhook,
      email), but its `incident.resolved` event **still appears** in the
      dashboard timeline / `events` table.
- [ ] A child that was un-suppressed because it was still down when the parent
      resolved (`reEvaluateChild`) **does** page on its own subsequent
      resolution.
- [ ] The Slack sender never posts a standalone top-level message for a
      `resolved`/`reopened` event that has no stored thread state.
- [ ] `make test` green; `make lint` clean.

## Out of scope

- Redesigning correlation/rollup selection (`applyRollup`, depth cap,
  correlation window) — unchanged.
- Group-incident (`check_group_uid`) dedup — already correct; only verify it
  is not regressed.
- Changing the dashboard incident timeline / `events` rendering — the resolved
  event row must keep showing closure.
- Reworking the Slack "update original message in place on resolve" behavior —
  that path stays as-is for incidents that *do* have thread state.

## Open items to confirm during implementation

- This was diagnosed from the code paths plus the attached screenshot; the
  reporting Slack channel is in the SolidPing workspace and was not readable
  from the spec author's tooling. The reproduction above is the disambiguator:
  if it does **not** reproduce a duplicate, capture the actual incident rows
  (`SELECT uid, check_uid, caused_by_incident_uid, paging_suppressed, state,
  started_at, resolved_at FROM incidents WHERE ...`) and the
  `incidents/<uid>/slack/thread` state entries for the "Claude API" outage to
  pin whether the second message comes from a rolled-up child (most likely), a
  double-enqueued notification job for one incident, or a group/member
  interaction, and adjust the fix site accordingly.

**Status**: Todo | **Created**: 2026-06-24

## Implementation Plan

### Core change — `server/internal/handlers/incidents/service.go` (`emitEvent`)
The early-return gate at ≈line 1137 currently reads:
```go
if incident.PagingSuppressed && eventType != models.EventTypeIncidentResolved {
```
Drop the `&& eventType != models.EventTypeIncidentResolved` exception so the gate
becomes `if incident.PagingSuppressed {`. The event row is already created
unconditionally by `s.db.CreateEvent` *before* this switch (≈line 1129), so a
still-suppressed incident keeps recording its `resolved` (and any other lifecycle)
event but no longer fans out a notification / group notification / escalation.
Rewrite the comment at ≈line 1138–1139 from "Resolve still notifies so timeline
observers see closure" to "record event, do not page" (timeline observers see
closure via the already-created event row, not a paging channel).

Correctness rests on the existing rollup invariant (`rollup.go`
`reEvaluateChild`, ≈line 173): when a parent resolves, a child still down is
un-suppressed first (`paging_suppressed = false` + `IncidentReopened`), so by the
time that child later resolves its `PagingSuppressed` flag is already `false` and
it pages normally. Gating on the *current* flag value pages exactly the incidents
that were ever paged.

### Defense-in-depth — `server/internal/notifications/slack.go` (`Send`)
After the `threadEntry` fetch (≈line 42) and **before** the Slack client is
constructed (≈line 44), add a guard: when `payload.EventType` is
`eventTypeIncidentResolved` or `eventTypeIncidentReopened` and there is no stored
thread state (`threadEntry == nil || threadEntry.Value == nil`), `return nil`
without posting. There is no original message to update or thread under, so a
standalone bare "resolved"/"reopened" message is never correct. The existing
resolution/reopen branches (≈line 47, 52) already require non-nil thread state, so
this only affects the previously-buggy fall-through to `postNewMessage`.

### Tests
Note: the spec referenced an existing test
`TestManualResolveSuppressedRolledUpChildStillEmitsResolved`, but no test of that
name (nor any asserting "suppressed child still emits a resolved *notification*")
exists in the codebase today — the closest are the resolve tests in
`server/internal/handlers/incidents/resolve_test.go`, none of which pins the buggy
behavior. So rather than *updating* a non-existent buggy test, the corrected
contract is captured by the new tests below. The manual-resolve path
(`ResolveIncident`) is the test entry point that drives `emitEvent` with
`EventTypeIncidentResolved`.

- **Add** `TestManualResolveSuppressedRolledUpChildStillEmitsResolved`
  (in `resolve_test.go`) — name preserved per spec, but encoding the *new*
  contract: a `PagingSuppressed=true` child that is resolved **creates exactly one
  `incident.resolved` event row** but **enqueues zero notification jobs**.
- **Add** `TestRolledUpChildResolveDoesNotPageWhenSuppressed` — a second, focused
  guard on the same invariant (suppressed child resolve ⇒ 0 notification jobs, 1
  `incident.resolved` event row), built via `applyRollup` against a real
  parent+child dependency so the rollup attribution path is exercised.
- **Add** `TestUnsuppressedChildResolveStillPages` — a child whose
  `PagingSuppressed` is `false` (i.e. the state after `reEvaluateChild`
  un-suppresses it) resolves ⇒ one notification job per bound channel. Regression
  guard so the fix does not silence real standalone resolutions.
- **Add** `TestSlackSender_Send_ResolvedNoThreadStateDoesNotPost` (in
  `slack_test.go`) — `Send` with a `resolved` payload and a `mockDBService` that
  returns no thread state returns `nil` without falling through to
  `postNewMessage`/`client.PostMessage` (the Slack client has no httptest seam, so
  a fall-through would attempt a real network call; the early return is the
  observable contract). Mirror with a `reopened` variant.
- Keep all existing `resolve_test.go` / group-incident / slack tests green.

### QA & verification
`make build`, `make lint` (backend), `make test` (backend) all green/clean; then
an independent `Explore` audit against this plan before archiving.
