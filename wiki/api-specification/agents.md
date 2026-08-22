# Agents & Private Locations

Deported agents connect outbound over a WebSocket and execute checks from a
private region (a customer network, a branch office, a lab). This page is the
route surface only — the enrollment protocol, the Ed25519 key ceremony, the
threat model, and the operational guidance live in
[`../features/deported-agents.md`](../features/deported-agents.md).

## Agent transport

### GET /api/v1/agent/ws
The agent's long-lived control channel: job dispatch down, results and
heartbeats up. Registered outside the normal user-auth chain — agents are not
users.

Auth (agent credentials, two phases):
- **First connect**: a one-shot enrollment token presented as a bearer token.
  These tokens are prefixed `spe_` and are consumed on use — the agent
  registers its Ed25519 public key during this connect.
- **Every reconnect afterwards**: Ed25519 request signing, with the headers
  `X-Sp-Agent-Uid`, `X-Sp-Timestamp`, `X-Sp-Nonce`, and `X-Sp-Signature`. The
  timestamp bounds replay windows and the nonce makes each signature
  single-use.

### POST /api/v1/agent/attachments
The agent's binary upload path (spec 2026-08-21-01 §6): the control channel
above is JSON, so a screenshot sent through it would be base64 inside the frame
that carries results. Agents hold no object-storage credentials by design, so
the bytes come here and the server writes them.

Auth: **the same Ed25519 request signature as a reconnect** — `X-Sp-Agent-Uid`,
`X-Sp-Timestamp`, `X-Sp-Nonce`, `X-Sp-Signature`, ±5 min skew, cluster-wide
replay guard. No bearer token, no session, no PAT; a nonce-store failure
rejects the upload rather than opening a replay window.

Request: the raw bytes as the body (`Content-Type: image/png`), with the
attachment topic in `X-Sp-Attachment-Topic`. A header, not a query parameter,
so the topic and the entity uid it names never land in an access log. Body cap
4 MiB — an over-cap upload is refused (413), never truncated.

Response: `201` with `{ "fileUid": "..." }`. The agent references that uid from
its result instead of carrying the bytes.

**The topic is never trusted.** The org the file is written under comes from the
agent's own binding (or, for a system agent serving a shared cloud region, from
the incident row itself) — never from the request. For an `incidents/<uid>/<kind>`
topic the server verifies, in order, that the kind is on the allowlist, that the
incident exists, that (for an org agent) it belongs to this agent's org, and
that its check is one this agent's **region** actually serves. Any failure
returns one undifferentiated `403 BAD_ATTACHMENT_TOPIC`: telling a probing agent
which incident uids exist in other tenants would be an oracle. The real reason
is logged server-side.

Content type is enforced against the **bytes**, not the header: the declared
type must be on the allowlist *and* the magic bytes must agree. Believing an
agent's Content-Type would let a caller store an HTML document as `image/png`
and have it served back from our own origin.

Rate limited **per agent** (burst 10, one token refilled per minute), keyed on
the authenticated identity rather than the IP so a fleet behind one NAT neither
shares nor can exhaust another agent's quota.

Topic entities are a registry, not a switch: a future attachment kind registers
a validator instead of adding a second endpoint. Unregistered entities are
rejected — the registry is fail-closed.

**Not yet wired:** the server does not currently send the WS upload-request
frame that would tell an agent *when* to upload (§6's `screenshotAvailable`
marker and the capture-id round trip). The endpoint and its authorizer are the
settled contract; the frame that drives them is follow-up work. Until then an
agent-side capture has no trigger, and an agent upload is recorded with
`trigger: "agent-upload"` rather than an incident transition it cannot prove.

## Private regions

A private region is the location label that agents attach to and that checks
target. Auth: org **admin**.

### GET /api/v1/orgs/:org/private-regions
List the org's private regions.

### POST /api/v1/orgs/:org/private-regions
Create a private region.

### DELETE /api/v1/orgs/:org/private-regions/:slug
Delete a private region. Note the path parameter is the region **slug**, not a
uid.

## Enrollment tokens

One-shot `spe_` tokens that let a new agent register itself. Auth: org
**admin**.

### GET /api/v1/orgs/:org/agent-enrollment-tokens
List outstanding enrollment tokens (the secret itself is only returned once, at
mint time).

### POST /api/v1/orgs/:org/agent-enrollment-tokens
Mint an enrollment token. The response carries the plaintext token — this is
the only time it is available.

### DELETE /api/v1/orgs/:org/agent-enrollment-tokens/:uid
Revoke an unused enrollment token.

## Agent inventory

Auth: org **admin**.

### GET /api/v1/orgs/:org/agents
List the org's enrolled agents, with their region, connection state, and last
heartbeat. Each row carries `version` — the agent's self-reported build
version (spec 2026-08-19-07), resolved from its `workers` row (the
`workers.version` column is the single source of truth; see
[../database-model/checks.md](../database-model/checks.md)). `null` means
"never reported" — an agent predating this feature, or one that has not sent
a claim frame yet — and must be rendered as **unknown**, never as drifted.
Detection only: nothing about this field gates, throttles or disconnects an
agent.

### DELETE /api/v1/orgs/:org/agents/:uid
Deregister an agent. Its key is invalidated and further connects are refused.

## Fleet-wide agent view

Auth: **superadmin** (not org-scoped — this is the only surface that can see
system agents, which have no owning org).

### GET /api/v1/system/agents
List every non-deleted agent across every organization, both org-enrolled and
platform-operated `kind=system` agents. Each row carries `kind` (`org` |
`system`) and `org` (owning org slug, `null` for system agents) in addition to
the fields returned by the org-scoped list above. Read-only: there is no
revoke action here — an org agent is revoked from its own org, and a system
agent is retired by removing its token from
`SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`.
