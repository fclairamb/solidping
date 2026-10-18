---
sidebar_position: 28
title: AI agent (A2A)
---

# Send incidents to an AI agent (A2A)

SolidPing can hand every incident to an AI agent that investigates it. It talks to the agent
over [A2A (Agent2Agent)](https://a2a-protocol.org), the open protocol for one agent to send a
task to another. SolidPing is the A2A client. The agent investigates with SolidPing's
[MCP server](./mcp.md) and its findings land on the incident timeline as comments.

## What the agent receives

One message per incident event: **opened** (and reopened), **resolved**, **acknowledged** and
**unacknowledged**. Comments, reminders, escalations, degraded and SLO burn alerts are not sent.

- All the messages of one incident share one conversation: the A2A `contextId` is the
  incident UID. Each event starts a new task in that conversation.
- The `messageId` is derived from the incident and the event, so a retried delivery reuses
  it and the agent can drop the duplicate.
- The message has two parts:
  - a text part: check name, status, failure reason, start time, links to the incident and
    the check, your instructions, and a line telling the agent it can use SolidPing's MCP tools;
  - a data part with the ids the MCP tools take:

```json
{
  "incidentUid": "…",
  "checkUid": "…",
  "checkSlug": "api-health",
  "orgSlug": "acme",
  "eventType": "incident.created",
  "status": "active",
  "failureReason": "HTTP 500",
  "startedAt": "2026-10-18T09:12:00Z",
  "resolvedAt": "",
  "dashboardUrl": "https://solidping.acme.com/d/orgs/acme/incidents/…",
  "mcpUrl": "https://solidping.acme.com/api/v1/mcp"
}
```

SolidPing sends with `returnImmediately: true`, so it never waits for the investigation to
finish.

## What comes back

- An agent that answers with a **message**: its text is recorded as a comment.
- An agent that answers with a **task**: SolidPing polls it (`GetTask`) after 10 s, 20 s,
  40 s, then every 60 s, for up to 30 minutes.
  - **Completed**: the text artifacts are recorded as a comment.
  - **Failed**, **canceled**, **rejected**: a short status line, plus the agent's status
    message if it gave one.
  - **Input required**, **auth required**: the agent's question, flagged as waiting for a
    human. SolidPing does not answer it.
  - Still working after 30 minutes: a note says SolidPing stopped waiting.

Comments are authored by the integration ("AI agent (A2A): &lt;integration name&gt;"). They
fan out to the incident's other channels like any comment, and are never sent back to the
agent. The agent's text is treated like a user comment: escaped in the dashboard and in every
channel. Data and file parts are ignored, and a reply longer than 4096 bytes is truncated.

## Set it up

1. Go to **Integrations** → **New** → **AI agent (A2A)**.
2. **Agent Card URL**: the full URL of the agent's card, or just its origin
   (`https://agent.acme.com`), to which `/.well-known/agent-card.json` is added.
3. **Authentication**: how SolidPing authenticates to the agent.
   - **None**.
   - **Bearer token**: sent as `Authorization: Bearer <token>`.
   - **API key header**: a header name (`X-Api-Key`) and its value.

   The token and the API key are stored encrypted and never shown again.
4. **Instructions** (optional): text added to every message, for example
   "Investigate and report your findings, do not restart anything".
5. Click **Test**. SolidPing fetches the card and shows the agent name, its skills and the
   protocol version it will use. Nothing is sent to the agent.
6. Save, then attach the integration to the checks the agent should look at, like any
   notification channel.

## Give the agent a SolidPing MCP token

The agent needs its own access to SolidPing to investigate. Create a token with the `mcp:read`
scope, so the agent can read but not change anything. The dashboard has no scope picker yet,
so use the API:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"AI agent","scopes":["mcp:read"]}' \
  https://solidping.acme.com/api/v1/orgs/acme/tokens
```

Configure the agent's MCP client with the `mcpUrl` it receives in every message and the token
as a bearer token. See [Headless agents](./mcp.md#headless-agents--ci). The tools it will use
most are `get_incident`, `diagnose_check` and `list_results`, with the ids of the data part.

## Protocol details

- A2A 1.0: SolidPing uses the first `supportedInterfaces` entry whose `protocolBinding` is
  `JSONRPC`, and sends `A2A-Version: 1.0` on every call.
- A2A 0.3 cards (`url` + `preferredTransport`) are supported too, with the 0.3 method names
  and no version header.
- A card without a JSONRPC interface, a card that cannot be fetched or parsed, a 4xx answer
  or a JSON-RPC error is a permanent failure. A 429, a 5xx, a timeout or a network error is
  retried.
- The card URL and the interface URL the card advertises both go through the
  [egress policy](../configuration/egress-policy.md): an agent card cannot point SolidPing at
  a private address.
- The resolved card is cached for 5 minutes per integration.
- Push notifications (A2A webhooks) are not used: SolidPing polls.
