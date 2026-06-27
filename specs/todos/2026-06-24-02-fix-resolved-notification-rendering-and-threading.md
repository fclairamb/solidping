# Fix: incident "resolved" Slack notifications are unrelated to the incident and render duplicated content

## Context

Incident "resolved" notifications in Slack are broken in two visible ways, both
reproduced in the reporting channel (`C0A65GHTC3Z`, screenshots attached to the
request). A batch of recoveries produces a column of messages, each rendered as:

```
✅ Incident resolved after 28 minutes.            ← message `text`
  ▏ ✅ Incident resolved after 28 minutes.        ← green attachment, SAME text
  ▏ Ajouté par SolidPing (dev)
```

Three operator complaints, two underlying defects:

1. **"They are not related to the initial incident."** Each resolved message is
   a **standalone top-level post** — no thread back to the "New incident for X"
   message, no monitor name, no link. You cannot tell *which* monitor recovered.
2. **"They show duplicate content."** Each message renders the same line
   **twice** — once as the message body, once inside a green attachment.
3. **"The green attachment is useless."** The attachment only repeats the body
   text; its sole effect is a redundant green border.

Reported in Slack:
- https://solidping.slack.com/archives/C0A65GHTC3Z/p1782249099331199
- https://solidping.slack.com/archives/C0A65GHTC3Z/p1782250119447319
- Follow-up screenshot (batch of ~8 identical "resolved after 28 minutes").

> Earlier framing note: the first screenshot was initially read as *two separate
> notifications* (a threaded reply plus a standalone). The follow-up makes clear
> it is **one message per resolution** that (a) duplicates its own content and
> (b) is posted standalone. This spec supersedes that framing.

SolidPing has two "incident grouping" mechanisms whose whole point is to turn a
correlated outage into **one** page and **one** resolution — group incidents
(`2026-04-29-04-group-incident-correlation.md`) and cascade rollup
(`2026-05-03-57-check-dependencies-and-cascade-rollup.md`). Defect B below is
where rollup leaks; Defect A is channel-rendering and is independent of grouping.

---

## Defect A — resolved message renders its content twice (useless green attachment)

### Root cause

`buildIncidentResolvedThreadReply` in
`server/internal/notifications/slack.go` (≈line 395) builds the message with the
*same string* in both the top-level `Text` **and** an attachment block:

```go
text := fmt.Sprintf(":white_check_mark: Incident resolved after %s.", duration)
blocks := []slack.Block{ {Type: section, Text: {Mrkdwn, text}} }
return &slack.MessageResponse{
    Text: text,                                              // rendered first
    Attachments: []slack.Attachment{
        {Color: colorSuccess, Fallback: text, Blocks: blocks}, // rendered again
    },
}
```

`Client.PostMessage` (`server/internal/integrations/slack/client.go` ≈line 112)
serializes **both** `text` and `attachments` into the `chat.postMessage`
payload, so Slack shows the line, then the green attachment with the identical
line. The green border carries no extra information — the `✅` already signals
success.

`buildIncidentReopenedThreadReply` (≈line 700) has the **same** `Text` +
identical-attachment shape and the same bug for the reopen message.

### Fix

Render the status line **once**, and make it self-contained so it is meaningful
even when not threaded (see Defect B). Concretely, return a single top-level
message with the monitor named and linked, and **no** attachment:

```go
checkName := getCheckName(payload.Check)
checkURL  := checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check)
text := fmt.Sprintf(":white_check_mark: %s — incident resolved after %s.",
    slackLink(checkURL, checkName), duration)
return &slack.MessageResponse{Text: text}
```

- Drop the redundant `Attachments` (kills the duplicate + the "useless green"
  border). If a success color is still wanted later, it must hold *different*
  content than the body — not a verbatim copy.
- Do the equivalent for `buildIncidentReopenedThreadReply` (single rendering,
  monitor named).
- Keep the `✅` / `⚠️` emoji prefix for the at-a-glance status cue.

---

## Defect B — resolved messages are not related to the opening incident

### Symptom

Resolved messages are posted as **standalone top-level messages** with no thread
link to the original "New incident for X" message and no monitor context. The
batch of ~8 identical "resolved after 28 minutes" with no opening message in
view is the tell: these resolutions have **no Slack thread state**, so they take
the "post a brand-new message" path.

### Root cause

The Slack sender only threads/updates a resolution when thread state exists.
`SlackSender.Send` (`server/internal/notifications/slack.go` ≈line 32):

```go
if payload.EventType == eventTypeIncidentResolved && threadEntry != nil && threadEntry.Value != nil {
    return s.handleIncidentResolution(...) // updates original + threads the reply
}
...
return s.postNewMessage(...) // threadEntry == nil ⇒ NEW top-level message, no context
```

Thread state (`incidents/<incidentUID>/slack/thread`) is only stored when the
incident's **opened** message was posted. An incident whose `opened` was never
sent therefore resolves into a context-free top-level post. The dominant way
that happens is **cascade rollup**:

- A rolled-up child incident is created with `paging_suppressed = true` and its
  `opened` notification is skipped — `emitEvent`
  (`server/internal/handlers/incidents/service.go` ≈line 1137) returns early for
  suppressed `created`/`escalated`/`reopened` events, so no Slack `opened`
  message and **no thread state**.
- But that same early-return **exempts `resolved`**
  (`&& eventType != models.EventTypeIncidentResolved`): a still-suppressed child
  *does* fan out its resolved notification (comment: "Resolve still notifies so
  timeline observers see closure"). This asymmetry is intentional today — it is
  even pinned by `TestManualResolveSuppressedRolledUpChildStillEmitsResolved`
  (`2026-05-07-01-fix-manual-incident-resolve-missing-notifications.md`).
- With no thread state, that resolved goes through `postNewMessage` → standalone
  top-level, no link. A shared dependency clearing makes all its rolled-up
  children recover together → the batch of orphan "resolved" messages.

Group incidents (`check_group_uid`) are **not** the trigger: a group is a single
incident and its `opened` posts thread state normally. The orphans come from the
**separate child incidents** of cascade rollup (and, more generally, from any
incident that resolves without ever having posted an `opened` message).

### Fix

Two complementary changes; do both.

1. **Don't page a still-suppressed incident on resolve.** In `emitEvent`, keep
   *recording* the `incident.resolved` event row (the dashboard timeline still
   shows closure) but gate the **notification fan-out** on the *current*
   `incident.PagingSuppressed` for **all** lifecycle events — i.e. drop the
   `&& eventType != EventTypeIncidentResolved` exception. This is safe for the
   load-bearing case: a child still down when its parent resolves is
   un-suppressed first by `reEvaluateChild`
   (`server/internal/handlers/incidents/rollup.go` ≈line 173), which flips
   `paging_suppressed = false` and emits `IncidentReopened` (pages then); by the
   time it later resolves it is no longer suppressed and pages correctly.

   | Scenario | Opened page | Resolved page (after fix) |
   |---|---|---|
   | Root / standalone incident | yes | yes (threaded under its opened message) |
   | Child rolled up, recovers before/with parent | no | **no** (was: orphan top-level) |
   | Child rolled up, still down at parent resolve | no → un-suppressed → paged | yes (now a real standalone incident) |

2. **Make any resolved that *does* page relate to its incident.** Belt-and-suspenders for any other path that resolves without thread state:
   - Self-contained body (Defect A fix already names + links the monitor), so
     even a non-threaded resolved says *which* monitor recovered.
   - In `SlackSender.Send`, when the event is `resolved`/`reopened` and there is
     **no** stored thread state, prefer **not** to post a bare top-level message
     (it is the symptom we are removing). At minimum it must be the
     self-contained form from Defect A — never the duplicated/contextless one.
   - Verify `handleIncidentResolution` *is* reached for normal incidents (thread
     state present): it updates the original "New incident" message to resolved
     and threads the reply. If real (non-rollup) incidents are landing in
     `postNewMessage`, find why their thread state is missing (e.g. opened send
     failed, or the state key/lookup drifted) and fix that too.

---

## Reproduction

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Defect A (always reproduces): any incident that resolves and notifies Slack
# shows the line twice (body + green attachment).
#   1. Bind a Slack channel to a check, force it down, let the incident open,
#      then fix it. Observe the resolved message: text duplicated, green border.

# Defect B (orphan standalone resolutions): rolled-up children.
PARENT=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"dep-parent","type":"http","config":{"url":"http://127.0.0.1:1"},"period":"5s","incidentThreshold":1}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq -r '.uid')
for c in child-a child-b child-c; do
  CUID=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"slug\":\"dep-$c\",\"type\":\"http\",\"config\":{\"url\":\"http://127.0.0.1:1\"},\"period\":\"5s\",\"incidentThreshold\":1}" \
    'http://localhost:4000/api/v1/orgs/default/checks' | jq -r '.uid')
  curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"parentCheckUid\":\"$PARENT\",\"kind\":\"hard\"}" \
    "http://localhost:4000/api/v1/orgs/default/checks/dep-$c/dependencies"
done
# Wait: ONE "incident opened" message (parent); children roll up silently.
# Fix all four checks so they recover together:
for slug in dep-parent dep-child-a dep-child-b dep-child-c; do
  curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"config":{"url":"https://example.com"}}' \
    "http://localhost:4000/api/v1/orgs/default/checks/$slug"
done
# BUG: a stack of standalone "Incident resolved after X" messages (one per child),
#      each with duplicated content. EXPECTED: one resolved, threaded under the
#      parent's incident message, content rendered once, monitor named.
```

## Files to edit

### `server/internal/notifications/slack.go`
- `buildIncidentResolvedThreadReply` (≈line 395): render once (drop the
  duplicate `Text`/attachment); name + link the monitor so it is self-contained.
- `buildIncidentReopenedThreadReply` (≈line 700): same single-render fix.
- `Send` (≈line 32) / `postNewMessage` (≈line 90): for `resolved`/`reopened`
  with no thread state, do not emit a bare contextless top-level message.

### `server/internal/handlers/incidents/service.go`
- `emitEvent` (≈line 1133–1155): drop the resolved exception from the
  paging-suppression gate so a still-suppressed incident records its event but
  does **not** fan out a notification on resolve. Update the comment.

### Tests
- **Update** `TestManualResolveSuppressedRolledUpChildStillEmitsResolved` — its
  expectation is now the bug. New contract: a still-suppressed child's resolve
  creates the event row but enqueues **no** notification job.
- **Add** `TestRolledUpChildResolveDoesNotPageWhenSuppressed`.
- **Add** `TestUnsuppressedChildResolveStillPages` (regression guard for the
  reEvaluateChild path).
- **Add** a Slack-sender unit test: resolved message contains the status line
  **exactly once** (no attachment duplicating `Text`) and includes the monitor
  name; reopened likewise.
- **Add** a Slack-sender test: resolved with no stored thread state does not
  post a bare contextless top-level message.
- Keep existing per-check and group-incident resolution tests green.

## Acceptance criteria

- [ ] A resolved Slack message renders the status line **once** — no green
      attachment repeating the body. Same for reopened.
- [ ] A resolved message **names the monitor** (and links to it / the incident),
      so it is clearly related to the incident even read in isolation.
- [ ] For a normal incident, the resolution **threads under** (and updates) the
      original "New incident for X" message — not a standalone post.
- [ ] A rolled-up child whose `opened` was suppressed produces **no** standalone
      "resolved" page on any channel (Slack/Discord/Google Chat/webhook/email),
      while its `incident.resolved` event still shows in the dashboard timeline.
- [ ] A child un-suppressed because it was still down at parent resolve still
      pages on its own later resolution.
- [ ] `make test` green; `make lint` clean.

## Out of scope

- Redesigning correlation/rollup selection (`applyRollup`, depth cap, window).
- Group-incident (`check_group_uid`) dedup — already correct; only verify it is
  not regressed.
- Reworking the rich "update original message in place on resolve"
  (`buildResolvedUpdateMessage`) beyond removing any duplicate-content shape.
- Changing the dashboard timeline / `events` rendering — the resolved event row
  must keep showing closure.

## Open items to confirm during implementation

- Diagnosed from code paths + the attached screenshots; the reporting channel is
  in the SolidPing workspace and was not readable from the spec author's
  tooling. Defect A is certain from the builder code. For Defect B, if the
  reproduction does not yield orphan standalone resolutions, capture the incident
  rows (`SELECT uid, check_uid, caused_by_incident_uid, paging_suppressed, state,
  started_at, resolved_at FROM incidents WHERE ...`) and the
  `incidents/<uid>/slack/thread` state entries for the affected batch to pin
  whether the orphans are rolled-up children (most likely) or another
  thread-state gap, and adjust the routing fix accordingly.

**Status**: Todo | **Created**: 2026-06-24 | **Updated**: 2026-06-26

---

## Implementation Plan

### Pre-implementation findings (state of `batch/2026-06-23` at start)

Defect B is **already fully implemented** on this branch (landed with the related
paging-suppression work):

- `emitEvent` (`server/internal/handlers/incidents/service.go` ≈line 1134-1146)
  already gates the notification fan-out on the *current* `incident.PagingSuppressed`
  for **all** lifecycle events — the `&& eventType != EventTypeIncidentResolved`
  exception is gone, the event row is still recorded via `CreateEvent`, and the
  comment already describes the new contract (un-suppression via `reEvaluateChild`).
- `SlackSender.Send` (`server/internal/notifications/slack.go` ≈line 44-54) already
  skips posting a `resolved`/`reopened` event that has **no** stored thread state,
  instead of falling through to `postNewMessage`.
- Tests already present and asserting the new contract:
  `TestManualResolveSuppressedRolledUpChildStillEmitsResolved` (now pins "records
  event, pages nothing"), `TestRolledUpChildResolveDoesNotPageWhenSuppressed`,
  `TestUnsuppressedChildResolveStillPages`
  (`server/internal/handlers/incidents/resolve_test.go`), and
  `TestSlackSender_Send_ResolvedNoThreadStateDoesNotPost`
  (`server/internal/notifications/slack_test.go`).

Therefore the remaining work is **Defect A only** (plus verifying Defect B is not
regressed and is covered).

### Step 1 — Defect A: `buildIncidentResolvedThreadReply`

`server/internal/notifications/slack.go` (≈line 396). Render the status line once,
self-contained (monitor named + linked), drop the redundant attachment:

```go
checkName := getCheckName(payload.Check)
checkURL  := checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check)
text := fmt.Sprintf(":white_check_mark: %s — incident resolved after %s.",
    slackLink(checkURL, checkName), duration)
return &slack.MessageResponse{Text: text}
```

Keep the `✅` cue. No `Blocks`, no `Attachments` (kills the duplicate body + the
useless green border). `slackLink` already falls back to plain text when the URL is
empty, so the monitor name is always present even without a base URL.

### Step 2 — Defect A: `buildIncidentReopenedThreadReply`

`server/internal/notifications/slack.go` (≈line 702). Same single-render fix; keep
the `⚠️` cue, name + link the monitor, drop the attachment:

```go
text := fmt.Sprintf(
    ":warning: %s — incident reopened (relapse #%d). Recovery requires the check to stay up for %d seconds.",
    slackLink(checkURL, checkName), relapseCount, payload.Check.RecoveryPeriodSeconds)
return &slack.MessageResponse{Text: text}
```

### Step 3 — Tests (notifications package)

- **Update** `TestSlackSender_buildMessage` resolved case: it currently asserts
  `msg.Attachments` is non-empty — that is now the bug. Make the assertion
  event-type aware (resolved ⇒ no attachments; created/escalated ⇒ attachments).
- **Add** `TestSlackSender_buildIncidentResolvedThreadReply_RenderedOnce`: the
  status line appears exactly once (no attachment duplicating `Text`), `Attachments`
  is empty, the monitor name is present, and the dashboard link is present when a
  base URL is configured.
- **Add** `TestSlackSender_buildIncidentReopenedThreadReply_RenderedOnce`: same
  assertions for the reopened reply (single render, monitor named, no attachment).

### Step 4 — QA + completeness audit

`make build-backend lint-back test`; fix code to green (never relax
`.golangci.yml`). Re-read spec; confirm every acceptance criterion is satisfied and
tested. Defect B criteria are confirmed against the already-present code + tests.

### Out of scope (unchanged)

`buildResolvedUpdateMessage` / `buildReopenedUpdateMessage` (the "update the
original message in place" builders) keep their rich attachment — they are not a
verbatim copy of a top-level `Text` and are exercised by `TestSlackSender_DashboardLinks`.
