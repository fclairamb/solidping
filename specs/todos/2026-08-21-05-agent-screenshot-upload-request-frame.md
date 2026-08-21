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
