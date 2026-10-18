---
model: opus
effort: high
---

# Send incidents to an AI agent over the A2A protocol

## Problem
There is no standard way for SolidPing to hand an incident to an AI agent that can
investigate it. Today the only options are a generic webhook or a chat message, which an
agent has to be custom-wired to read.

MCP doesn't solve it: the agent always opens the connection, so SolidPing's MCP server
can't wake an agent. Claude Code channels are Claude-only, a research preview, and need a
local stdio process plus an already open session. CloudEvents is only a payload format.

A2A (Agent2Agent) is the open standard built for this: a client sends a message
to an agent described by an Agent Card. SolidPing should be that client. The agent then
investigates through SolidPing's existing MCP server (`diagnose_check`, `get_incident`,
`list_results`), and its findings come back on the incident timeline.

Naming: SolidPing already uses "agent" for deported check agents (`server/internal/agents`,
`server/internal/agentmode`). This feature is "A2A" in code (`a2a`) and "AI agent (A2A)" in
the UI. Never plain "agent".

## Proposal
1. Add `ConnectionTypeA2A ConnectionType = "a2a"` next to the others in
   `server/internal/db/models/integration.go:29`, with `CanNotify: true, CanSource: false` in
   the capability table read by `CapabilitiesFor` (`integration.go:91`).
2. Settings (stored on the integration row like other senders):
   - `agent_card_url`: full card URL, or a bare origin to which
     `/.well-known/agent-card.json` is appended. Accept a full URL because deployments
     don't all serve the card at the same path.
   - `auth`: `none`, `bearer` (token) or `api_key` (header name + value). Secrets go
     through the same handling the webhook sender uses (`webhook_secret.go`). OAuth2 client
     credentials are out of scope for now.
   - `instructions` (optional): free text added to every message, e.g. "Investigate and
     report your findings, do not restart anything".
3. Protocol: A2A spec v1.0.1 (wire version `1.0`), through
   `github.com/a2aproject/a2a-go/v2`, which targets v1.0. `google.golang.org/grpc` is
   already a direct dependency (`server/go.mod:333`), so the SDK should add little.
   - Use the first `supportedInterfaces` entry with `protocolBinding: JSONRPC`, and send
     `A2A-Version: 1.0` on every RPC (an absent header means 0.3).
   - v0.3: support it only if the SDK's `a2acompat/a2av0` package makes it a thin adapter
     (the card's interface `protocolVersion` picks the codec). If it needs more than that,
     ship v1.0 only: a 0.3-only card is a permanent error ("this agent only speaks A2A
     0.3"), shown by the Test button too.
   - All A2A HTTP traffic (card fetch, `SendMessage`, `GetTask`) goes through SolidPing's
     egress guard. That covers the URL the user typed and also the interface URL the card
     advertises, which is untrusted. Validate both with `ValidateSenderURL`
     (`urlvalidate.go:41`), and give the SDK the guarded `http.Client` so redirects and DNS
     answers are checked as well.
4. New `server/internal/notifications/a2a.go` with `A2ASender` implementing `Sender`
   (`sender.go:84`), registered in `senderFactories()` (`registry.go:108`):
   - Events: incident opened, resolved, acknowledged and unacknowledged. Add
     `models.ConnectionTypeA2A: {NotifiesComments: false, NotifiesAcks: true,
     NotifiesReminders: false}` to `senderCapabilities()` (`registry.go`). Any other event
     type that reaches the sender (degraded, SLO burn, check deleted...) returns nil
     without a request.
   - Cache the resolved card per integration with a short TTL.
   - `SendMessage` with `returnImmediately: true`: v1.0 blocks by default until the task
     ends, which would hold the notification job for the whole investigation.
   - One conversation per incident: `contextId` = incident uid, and no `taskId`. v1.0
     rejects a message sent to a task that already ended (`UnsupportedOperationError`), so
     each event starts a new task in the same context.
   - `messageId` is derived from the incident event that triggered the notification (a
     hash of incident uid + event id), so a notification job retried after a successful
     send reuses the same id and the agent can drop the duplicate instead of starting a
     second investigation.
   - Message parts:
     - a text part: human summary (check name, status, failure reason, started at, links)
       plus `instructions`, plus one line telling the agent it can call SolidPing's MCP
       tools with the ids below;
     - a data part: `{incidentUid, checkUid, checkSlug, orgSlug, eventType, status,
       failureReason, startedAt, resolvedAt, dashboardUrl, mcpUrl}`.
   - Errors, like `classifyPagerdutyResponse` (`pagerduty.go:330`): 5xx, timeouts and
     network errors are retryable (`IsNetworkError`, `sender.go:115`). 4xx and JSON-RPC
     errors (including `VersionNotSupportedError`) are permanent.
   - Response handling:
     - a `Message`: record it as a comment (step 6);
     - a `Task` already in a terminal or interrupted state: record its result (step 6);
     - a `Task` in `SUBMITTED` or `WORKING`: enqueue the poll job (step 5).
5. Polling is a chain of short jobs: each run makes one `GetTask` call, then either
   finishes or enqueues its successor. No run waits, and the whole state lives in the job
   payload, so a restart or a deploy mid-investigation loses nothing (no new table, no
   migration). Add `JobTypeA2ATaskPoll JobType = "a2a_task_poll"` next to
   `JobTypeNotification` (`server/internal/jobs/jobdef/types.go:25`), with a run in
   `server/internal/jobs/jobtypes/job_a2a_task_poll.go`.
   - The sender enqueues the first run 10 s after the send, with
     `jctx.Services.Jobs.CreateJob(ctx, orgUID, "a2a_task_poll", payload,
     &jobsvc.JobOptions{ScheduledAt: &t})` (`server/internal/jobs/jobsvc/service.go:46`).
   - Payload: org uid, integration uid, incident uid, A2A task id, attempt, deadline.
   - Each run calls `GetTask`:
     - `COMPLETED`, `FAILED`, `CANCELED`, `REJECTED` (terminal) or `INPUT_REQUIRED`,
       `AUTH_REQUIRED` (interrupted): record the result (step 6) and stop.
     - `SUBMITTED`, `WORKING`: reschedule itself with
       `jctx.Services.Jobs.CreateJob(..., &jobsvc.JobOptions{ScheduledAt: &t})`, as
       `job_custom_domain_verify.go:233-244` does. Backoff: 10 s, 20 s, 40 s, then every
       60 s, so findings show up at most a minute late (about 30 requests per event).
     - Past the deadline (30 min after the first send): add a timeline note "AI agent still
       working after 30 min, stopped waiting" and stop.
     - 5xx or network error: count the attempt and reschedule. 4xx (task not found,
       auth): log a warning and stop, no comment.
     - Integration or incident deleted in the meantime: stop silently.
   - A2A push notifications are out of scope. Push is optional on the agent side, so this
     polling is needed anyway; push can be added on top later if someone needs sub-minute
     latency.
6. Recording replies on the timeline: add `AddCommentFromA2A` next to
   `AddCommentFromDiscord` (`server/internal/handlers/incidents/service.go:3287`), authored
   by the integration ("AI agent (A2A): <integration name>").
   - Text: the text parts of the agent message, plus the text artifacts of a completed
     task. For `FAILED`, `CANCELED`, `REJECTED`: a short status line plus the status
     message, if any. For `INPUT_REQUIRED`, `AUTH_REQUIRED`: the agent's question, flagged
     as waiting for a human (SolidPing doesn't answer it). Data and file parts are
     ignored. Truncate to the existing comment size limit.
   - The text is untrusted external content: it goes through the same escaping as a user
     comment, in the dashboard and in every channel the comment fans out to.
   - The comment fans out like any comment. A2A has `NotifiesComments: false`, so it never
     loops back to the agent.
7. Dashboard: add the integration form (card URL, auth, instructions) in `web/dash0/` and
   a "Test" button that resolves the card and shows the agent name, its skills and the
   protocol version it will use, before saving.
8. Docs: one page in `web/docs/` explaining the setup, and how to give the agent a SolidPing
   MCP token so it can investigate.

## Tests
- `server/internal/notifications/a2a_test.go`, with `httptest` serving a fake Agent Card
  and JSON-RPC endpoint:
  - opened incident: `SendMessage` with the text part, the data part,
    `contextId` = incident uid, no `taskId`, `returnImmediately: true`, and the
    `A2A-Version: 1.0` header;
  - resolved, acknowledged and unacknowledged reuse the same `contextId`;
  - the same event sent twice (notification job retry) uses the same `messageId`; two
    different events of the same incident use different ones;
  - a degraded event sends nothing and returns nil;
  - bearer and api_key send their header; `none` sends no auth header;
  - card 404, malformed card, or card without a JSONRPC interface: permanent error;
  - 0.3-only card: works through the compat codec (old method names, no version header),
    or returns the permanent "only speaks A2A 0.3" error if compat was dropped;
  - endpoint 503 and connection refused: retryable; 401 and a JSON-RPC error object:
    permanent;
  - card URL on a loopback or private address: rejected. Card on an allowed host whose
    interface URL points to a private address: rejected too (SSRF through the card);
  - `Message` response: one comment recorded. `Task` in `WORKING`: one poll job enqueued
    about 10 s later, no comment.
- `server/internal/jobs/jobtypes/job_a2a_task_poll_test.go`:
  - `WORKING` then `COMPLETED`: one comment with the artifact text, no further job;
  - `FAILED` and `REJECTED`: one status comment; `INPUT_REQUIRED`: one comment with the
    question, polling stops;
  - still `WORKING` at the deadline: the timeout note, no new job;
  - `GetTask` 503: rescheduled with the next backoff; 404: stops, no comment;
  - integration deleted: stops, no comment, no request.
- `server/internal/handlers/incidents` tests: `AddCommentFromA2A` attributes the comment to
  the integration; agent text containing HTML or markdown links is escaped exactly like a
  user comment (negative case).
- `server/internal/notifications/registry_test.go`: `GetSender(ConnectionTypeA2A)` returns
  an `A2ASender`; `CapabilitiesForSender(ConnectionTypeA2A)` has comments and reminders
  off, acks on.
- `server/internal/db/models/integration_test.go`: `a2a` row in the capability table
  (`canNotify: true, canSource: false`).

## To verify
- a2a-go v2: the `a2acompat/a2av0` API (can one client talk to a 0.3 agent with a thin
  adapter?), how to pass a custom `http.Client`, and how `returnImmediately` and
  `GetTask` are exposed. Check which new modules it pulls into `server/go.mod`.
- How other senders use the egress guard beyond `ValidateSenderURL`
  (`notifications/egress_test.go`), so the SDK's client gets the same dial-time checks.
- Where integration secrets are stored and masked in API responses (`webhook_secret.go`,
  the PagerDuty routing key), and reuse that.
- The comment size limit, and the escaping path used for comments from Slack and Discord.
- Which event types reach a sender, and that returning nil for the skipped ones doesn't
  break delivery bookkeeping (`server/internal/jobs/jobtypes/job_notification.go`).
- Where integration forms live in `web/dash0/` (grep for `pagerduty`).
- Which id in the notification `Payload` identifies the triggering incident event and stays
  the same when the notification job is retried (for the `messageId`).
- That finished job rows get cleaned up: a poll chain creates up to about 30 rows per event.
