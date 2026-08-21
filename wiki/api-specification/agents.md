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
The WS route's sibling, and its counterpart: the socket is a JSON control
channel, this is the only way an agent gets **binary bytes** into storage
(spec 2026-08-21-01). Agents hold no object-storage credentials by design, so
the server writes the blob on their behalf.

Auth: the **same** Ed25519 signed headers as a WS reconnect
(`X-Sp-Agent-Uid` / `X-Sp-Timestamp` / `X-Sp-Nonce` / `X-Sp-Signature`, ±5 min
skew, cluster-wide replay guard). No bearer token, no second credential.

Request: raw body (`image/png` today, **sniffed** from the magic bytes rather
than believed from a header), with the attachment key in `?topic=`. The topic is
`<entity>/<uid>/<kind>`, e.g.
`incidents/9a1eb273-0a95-4d6b-b967-9af076c1f8e8/screenshot`. Per-file size cap
and a per-agent rate limit apply.

**Authorization never trusts the topic.** A prefix→authorizer registry resolves
the topic's entity; the `incidents/` authorizer requires the incident to exist,
derives the ORGANIZATION FROM THE INCIDENT ROW (never from the request), refuses
an org agent whose org is not the incident's, and refuses an agent whose region
does not serve the incident's check. An unregistered entity fails closed.

Responses: `201 {"fileUid": "…"}`; `401` for any authentication failure
(deliberately indistinguishable — no agent-existence oracle); `403` for a topic
the caller may not write under (also deliberately unspecific); `400` malformed
topic or empty body; `413` over the cap; `415` mime mismatch.

Note: the server does not yet ASK an agent for an upload — the WS
upload-request frame is a follow-up (spec 2026-08-21-05). The endpoint contract
above is settled.

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
