---
model: opus
effort: high
---

# Agent-side screenshot capture: the WS upload-request frame and the agent's capture LRU

## Problem

Spec [2026-08-21-01](2026-08-21-01-generic-attachments-and-incident-screenshots.md)
built the generic attachments rail and landed everything the SERVER side needs:

- `files.topic` / `files.details` plus the prefix-reap and GC primitives.
- `POST /api/v1/agent/attachments` — Ed25519 signed-header auth (same scheme as
  the WS upgrade), replay-guarded, per-agent rate-limited, magic-byte sniffed,
  with the `incidents/<uid>/…` topic authorizer that derives the org FROM the
  incident and refuses an agent whose region does not serve the check
  (`server/internal/handlers/attachments/`).
- The in-process capture path end to end: `checkbrowser` captures on a failing
  opted-in run, the incident pipeline persists it on open/reopen, the dashboard
  renders it.

It explicitly took its own §6 escape hatch for the last piece: **a deported
agent still has no way to get its capture to that endpoint.**

The endpoint contract is settled; what is missing is the *conversation* that
decides when to use it. `checkerdef.Screenshot` already carries the marker
fields the design calls for (`available`, `captureId`, `json:"-"` on `PNG`, so
the bytes can never be smuggled through the JSON control channel), and the
incident pipeline already declines to persist a marker that carries no bytes
(`persistScreenshot` in `server/internal/handlers/incidents/service.go`). So a
browser check running on a private agent captures nothing today: the agent has
no LRU, never sets the marker, and the server never asks for an upload.

## Proposal

Implement the flow spec 2026-08-21-01 §6 describes, in the order it describes:

1. **Agent-side capture + LRU.** The agent runs the same `checkbrowser` code, so
   the capture already happens; what is missing is somewhere to put the bytes.
   Add a small bounded LRU (a few entries, a few MiB total, TTL'd) keyed by a
   generated capture id. The bytes NEVER enter the result frame.
2. **Marker on the result frame.** The agent sets
   `Diagnostics.Screenshot = {available: true, captureId: "…"}` — the fields
   already exist and already serialize; nothing on the wire changes shape.
3. **Server → agent upload-request frame.** A new server-to-client frame type in
   `server/internal/agents/protocol.go`, emitted by the incident pipeline when
   (and only when) a result carrying the marker opens or reopens an incident.
   It names the capture id and the topic the agent must POST to.
   - The pipeline currently has no way to reach the agent's socket. It will need
     a hook interface (mirroring `AttachmentStore` / `PublicationHook`:
     small, nil-safe, best-effort) that `agentws` implements.
   - The agent may have reconnected, or be a different replica's connection, in
     the interval. Decide explicitly: request-on-best-effort and drop, or a
     short-lived pending-request record. Do NOT block the incident on it.
4. **Agent honours the request** by POSTing the bytes to
   `POST /api/v1/agent/attachments?topic=…` with its normal signed headers.

### Decisions to settle in the plan

- **What if the agent is gone?** A capture that cannot be uploaded is simply
  lost; the incident must open exactly as it does today.
- **Bounding the ask.** One upload request per incident transition, never per
  failing result. The server must not be able to be talked into requesting an
  unbounded number of uploads by a chatty agent's markers.
- **Trusting the marker.** `captureId` is agent-supplied. It names something in
  the agent's OWN cache, so it is not a security input — but the topic the
  server sends back must be server-generated, never echoed from the marker.

### Non-goals

- Changing the endpoint contract, the authorizer registry, or the topic grammar
  — those are settled by 2026-08-21-01 and have direct tests.
- Screenshots for HTTP checks; "after" screenshots on resolve.

## Tests

- Agent LRU: bounded by count AND bytes, evicts oldest, TTL'd.
- Marker round-trip: an agent result frame carries `available`/`captureId` and
  never the bytes (positive control: `failureResponse` still serializes).
- The upload-request frame is emitted on open/reopen only — proved with a
  flapping sequence that opens exactly one incident.
- An agent that disconnected between the result and the request: the incident
  still opens, nothing is retried forever.
- End-to-end: marker → request → POST → attachment visible on the incident.

---

## Implementation Plan

### Decisions carried in from the spec (not re-litigated)

- **Agent gone → the capture is simply lost.** Nothing on the incident path may
  block, fail, or retry because an agent could not be reached. The hook returns
  nothing at all, exactly like `PublicationHook`.
- **Bounding the ask: one request per incident TRANSITION.** The request is
  emitted from the same two call sites `persistScreenshot` already has —
  `createIncident` and `reopenIncident` — and from nowhere else. A chatty agent
  that sets the marker on every failing result therefore cannot make the server
  emit more than one request per open/reopen; the confirmation window and the
  incident state machine are what bound it, not a counter we could get wrong.
- **The topic is SERVER-GENERATED.** `attachments.IncidentScreenshotTopic(incident.UID)`
  built from the incident row the server just wrote. `captureId` is echoed back
  verbatim because it only names a slot in the agent's own RAM — it never
  reaches storage, a path, or a query.

### Step 3 sub-decision: request-on-best-effort-and-drop

**Chosen: best-effort, in-process, one shot, no pending record.**

Justification, in two lines: the request rides the socket the result arrived on,
and on the master the incident pipeline runs *synchronously inside*
`workers.SubmitResult`, so at the instant the request is emitted the agent's
connection is on this very replica and still open in ~all cases. A pending
record would buy the residual case (agent reconnected in the millisecond
between, or a later replica processes it) at the cost of a table, a retry loop
and an expiry sweeper — for an artifact whose source is a TTL'd in-RAM LRU that
has very likely already dropped the bytes by the time a retry fires. Losing the
screenshot is a papercut; the incident is unaffected either way.

Consequences accepted and pinned by tests: the send is a non-blocking channel
put that returns `false` when no connection is registered for that worker (and
is simply dropped), and the agent `Take`s the bytes out of its LRU, so a failed
POST is never retried.

### Steps

1. **`server/internal/agents/capturecache`** — a bounded, TTL'd LRU keyed by a
   generated capture id. Bounded by entry count AND total bytes; `Put` refuses a
   blob larger than the whole budget; `Take` removes (one-shot). Injectable clock.
2. **`agents/protocol.go`** — `MsgTypeUploadRequest = "upload-request"` plus
   `ServerFrame.Topic` / `ServerFrame.CaptureID`. Nothing on the client frame changes.
3. **`checkworker/backend/ws.go`** — on `SubmitResult`, move the screenshot bytes
   into the LRU and replace them, **on a copy of the caller's Diagnostics**, with
   `{available:true, captureId}`. A blob that does not fit advertises nothing.
4. **`handlers/agentws`** — a small `connRegistry` (worker uid → buffered
   outbound frame channel, registered for the life of one connection,
   identity-checked on removal so a stale teardown cannot unregister the live
   socket). The connection loop drains it in its existing `select`. `Handler`
   grows `RequestScreenshotUpload(ctx, workerUID, captureID, topic)`.
5. **`handlers/incidents`** — `AgentUploadRequester` hook, nil-safe, returning
   nothing; `persistScreenshot`'s "marker with no bytes" branch becomes the
   request instead of a bare `return`. Keyed on `result.WorkerUID`, which the
   agent's WS connection registers itself under.
6. **`checkworker/backend/ws.go`** — honour the frame: `Take` the bytes and POST
   them to `/api/v1/agent/attachments?topic=…` with the SAME Ed25519 signed
   headers the WS reconnect uses. One attempt, off the read pump's goroutine.
7. **Wiring** in `app/server.go`: `agentWorkerIncidents.SetAgentUploadRequester(agentWSHandler)`.
8. **Tests** — the spec's list, each as a real negative where one is stated.
