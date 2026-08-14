---
model: opus
effort: high
---

# Telegram contacts paged for an incident never hear that it resolved

## Problem

SolidPing has two delivery paths with asymmetric lifecycles:

1. **Channel connections** (Slack, Discord, webhooks, …) hear the full incident
   lifecycle: the `emitEvent` switch
   (`server/internal/handlers/incidents/service.go:1370`) queues notification
   jobs for `incident.created`, `incident.resolved`, `incident.escalated` and
   `incident.reopened`.
2. **Person contacts** (Telegram, WhatsApp, SMS/voice) are paged only by the
   escalation-step job, whose contract is "fire while the incident is
   unhandled": `incidentNeedsPaging`
   (`server/internal/jobs/jobtypes/job_escalation_step.go:172`) exits as soon
   as the incident is acked or resolved.

So the on-call person gets a red Telegram alert with an Acknowledge button,
maybe acks it — and then nothing, ever. They have to poll the dashboard (or the
`/incidents` bot command) to learn the incident ended. Worse, the original
alert stays red with a live-looking Acknowledge button forever.

The odd part: the Telegram side already has complete "resolved" rendering,
**currently unreachable**. `sendTelegramAlert`
(`server/internal/jobs/jobtypes/job_escalation_step_telegram.go:212-226`) can
send a RESOLVED notice threaded under the incident's first message, edit the
original red alert into its resolved form (`BuildResolvedOriginalHTML`,
`telegram.StateResolved` in
`server/internal/integrations/telegram/message.go`), and strip the Acknowledge
keyboard (`telegramAckKeyboard` returns nil for resolved incidents,
`job_escalation_step_telegram.go:168-174`). No caller can reach it because the
only dispatcher bails on resolved incidents. The renderer exists; the
dispatcher doesn't.

Two ready-made records of "who was actually paged" exist:

- **Thread anchors**: state entries keyed
  `telegram_msg:<incidentUID>:<chatID>` with a 7-day TTL
  (`job_escalation_step_telegram.go:25-29,276-278`) — written once per
  (incident, chat) on the first successful non-resolved alert
  (`recordTelegramThread`, `:315-340`). Exactly the chats that received an
  alert, with the message id to thread under. Prefix listing already exists:
  `ListStateEntries` (`server/internal/db/service.go:534`).
- **Audit rows**: `incident_notifications` rows written by `auditPhoneSend`
  with channel `telegram` and the sent message id
  (`job_escalation_step_telegram.go:105-106`). Durable, but keyed by user (not
  chat), so they need re-resolution through the user's current verified
  Telegram contact.

## Proposal

### 1. New generic job type: `incident_resolution_notice`

Add a job type (registered in
`server/internal/jobs/jobtypes/registry.go`) whose config carries
`{organizationUid, incidentUid}`. Name and shape are channel-generic; V1
implements Telegram only (WhatsApp has the same dormant resolved-rendering at
`job_escalation_step_whatsapp.go:169` and is an explicit follow-up, out of
scope here).

### 2. Enqueue it from the resolved hook in `emitEvent`

In the lifecycle switch (`service.go:1370`), on
`models.EventTypeIncidentResolved`, enqueue one `incident_resolution_notice`
job — **after** the `PagingSuppressed` early-return (rolled-up children never
paged, so they must not send resolution notices either;
`service.go:1374-1383`), and **before/independent of** the group-incident
branch: group incidents page through the same escalation machinery with the
group incident's UID, so anchors are keyed by that UID and the job works
identically for them. One hook covers auto-resolve (check recovery,
`resolveIncident`, `service.go:743`), manual resolve (`ResolveIncident`,
`service.go:2722`), and group incidents.

Follow the existing job-creation pattern used by `queueNotifications`
(`service.go:~1480-1495`): failure to enqueue logs a warning, never fails the
resolution.

### 3. Fan-out inside the job — anchors first, audit rows as fallback

For the incident:

1. **Primary: thread anchors.** `ListStateEntries` with prefix
   `telegram_msg:<incidentUID>:`; each entry yields a chatID (parsed from the
   key) and the anchor messageID. For each chat:
   - Reserve via the existing hourly runaway guard
     (`Entitlements.ReserveTelegram`), same as pages — a mass-recovery storm
     (network blip heals, dozens of incidents resolve at once) must not blast
     unbounded sends.
   - Send the resolved notice by reusing the existing send path with a
     RESOLVED-state `AlertParams` (state label from the now-resolved incident,
     duration available from `StartedAt`/`ResolvedAt`). Reuse or minimally
     refactor `sendTelegramAlert` + `telegramAlertParams` out of
     `EscalationStepJobRun` so both jobs share them — do not duplicate the
     threading / edit-original / retry-after logic. The existing code already:
     threads under the anchor, edits the original alert to
     `BuildResolvedOriginalHTML` with an **explicit empty keyboard** (removing
     the stale Acknowledge button), degrades to a standalone message when
     Telegram rejects the anchor, and honors `retry_after`.
   - **Delete the anchor after a successful send** (`DeleteStateEntry`). This
     is the idempotency mechanism (see §4).
2. **Fallback: audit rows** — only for chats not covered by an anchor (anchor
   expired past its 7-day TTL on a long incident, or the anchor write failed).
   Query `incident_notifications` for this incident with channel `telegram`
   and a sent status, resolve each distinct user's current verified Telegram
   contact to a chatID (skip users who unlinked since), and send a standalone
   resolved notice. Gate each with `SetStateEntryIfNotExists` on a marker key
   (e.g. `telegram_resolved:<incidentUID>:<chatID>`, short TTL ~7d) so
   retries and repeat resolutions can't double-send.

Per-chat failures are best-effort: log, continue with the remaining chats, and
return a retryable error only for network-class failures
(`notifications.IsNetworkError` / `telegram.ErrRateLimited` semantics) so the
job re-runs — already-notified chats have lost their anchor / gained their
marker, so a re-run cannot duplicate.

### 4. Reopen semantics — why anchor-deletion is the right idempotency

Reopened incidents deliberately do **not** restart the paging cycle
(`service.go:1399-1405`): a relapse never re-pages person contacts. So after
resolve → notice → anchor deleted, a reopen followed by a second resolution
finds no anchors and (thanks to the marker keys) no un-gated audit fallback —
**no second "resolved" message for a relapse the person never heard about**.
That is the desired behavior; a resolved notice for an invisible relapse is
pure noise. If a future spec makes reopens re-page, the new page recreates the
anchor and the next resolution notifies again — the mechanism self-heals.

### 5. Tests

Table-driven, `testify/require`, `t.Parallel()`, following the fake-Telegram
server harness in `job_escalation_step_telegram_test.go`:

- Resolved incident with one paged chat → notice sent, threaded under the
  anchor, original message edited with empty keyboard, anchor deleted.
- Two chats paged, first send fails with a network error → second chat still
  notified; job returns retryable; re-run notifies only the failed chat.
- No anchors + audit row present (TTL-expired case) → standalone notice via
  the contact lookup, marker written; second run sends nothing.
- Reopen relapse: resolve (notice sent), reopen, resolve again → exactly one
  notice total.
- `PagingSuppressed` child incident resolving → no job enqueued / no send.
- Runaway guard exhausted → send skipped with an audit-skip row, job does not
  fail.
- Group incident: anchors under the group incident UID → notices sent.

### Out of scope (explicit follow-ups)

- WhatsApp / SMS resolution notices (same job, next channel).
- "Acknowledged by X" edit — when someone acks from the dashboard, the
  Telegram alert keeps a stale Acknowledge button until resolution. The
  refactored shared send/edit path from §3 is the groundwork; the trigger
  would hang off `EventTypeIncidentAcknowledged`.

## Implementation Plan

### Step 1 — Extract the shared Telegram send path (no behavior change)

`job_escalation_step_telegram.go` currently hangs the whole send path off
`*EscalationStepJobRun` even though none of it reads run state. Turn each into
a free function taking `(ctx, jctx, log, …)` and keep a one-line method wrapper
so every existing call site and test is untouched (and the sibling multi-org
spec rebases trivially):

- `telegramAlertParamsFor(ctx, jctx, log, incident)`
- `sendTelegramAlertShared(ctx, jctx, log, client, incident, chatID, params)`
- `telegramThreadAnchorFor` / `clearTelegramThreadAnchorFor`
- `orgSlugForOrg(ctx, jctx, log, orgUID)` and `incidentCheckName(ctx, jctx, incident)`
  (the two helpers `telegramAlertParamsFor` needs, likewise currently methods)

### Step 2 — New job type `incident_resolution_notice`

- `jobdef.JobTypeIncidentResolutionNotice JobType = "incident_resolution_notice"`
- Registered in `jobtypes/registry.go`.
- Config `{organizationUid, incidentUid}`; both required at `CreateJobRun` time.

### Step 3 — The job body (`job_incident_resolution_notice.go`)

1. Load the incident. Bail (nil) when: it is `PagingSuppressed`, it is no longer
   resolved (a reopen beat the job — the notice would be a lie, and the anchors
   survive so the next resolution notifies), or Telegram is not configured on
   this instance.
2. Build the resolved `AlertParams` from the shared helper, overriding `Detail`
   with `resolved after <duration>` (`StartedAt` → `ResolvedAt`).
3. **Anchor pass.** `ListStateEntries(prefix "telegram_msg:<incidentUID>:")`;
   the chat id is the key suffix. Per chat:
   - claim the marker `telegram_resolved:<incidentUID>:<chatID>` with
     `SetStateEntryIfNotExists` (7d TTL) — not created ⇒ already notified, skip;
   - reserve `Entitlements.ReserveTelegram` — denied ⇒ release the marker,
     write a skipped audit row, continue (never fail the job);
   - send through `sendTelegramAlertShared`, which threads under the anchor,
     rewrites the original red alert with an explicitly empty keyboard, degrades
     to standalone on a rejected reply target and honors `retry_after`;
   - on failure: release the marker (so a retry can still deliver), remember
     whether the failure was network-class;
   - on success: delete the anchor and record a sent audit row.
4. **Fallback pass** — only for chats no anchor covered.
   `ListIncidentNotifications(incidentUID, status=sent)` filtered to
   `channel_type=telegram` **and** `event_type=incident.escalated` (that is what
   a page is audited as; our own notice rows carry `incident.resolved`, so the
   scan cannot feed on itself). Each distinct user's *current* verified Telegram
   contact is resolved through `ListUserContactsWithRoutes`; users who unlinked
   are skipped. Same marker/reserve/send flow, standalone (no anchor to thread
   under or edit).
5. Return a `jobdef.RetryableError` only when at least one chat failed with a
   network-class error (`telegram.ErrRateLimited`, `telegram.ErrRequestFailed`,
   `notifications.IsNetworkError`); every other per-chat failure is logged and
   swallowed. A re-run cannot duplicate: notified chats lost their anchor and
   kept their marker.

### Step 4 — Enqueue from the resolved hook

In `incidents/service.go` `emitEvent`, inside the lifecycle switch, **after**
the `PagingSuppressed` early return and **before** the group-incident branch:
on `EventTypeIncidentResolved`, create one `incident_resolution_notice` job.
Enqueue failures log a warning and never fail the resolution — same contract as
`queueNotifications`.

### Step 5 — Tests

New `job_incident_resolution_notice_test.go` on the existing fake-Bot-API
harness (`setupPhoneEnv` + `newFakeBotAPI`), plus one case in the incidents
service tests for the enqueue/suppression gate:

1. one paged chat → notice threaded under the anchor, original edited with an
   empty keyboard, anchor deleted, marker written;
2. two chats, first send fails (network class) → second still notified, job
   returns retryable, re-run notifies only the failed chat;
3. no anchors + audit row → standalone notice via the contact lookup, marker
   written, second run sends nothing;
4. reopen relapse (resolve → reopen → resolve) → exactly one notice;
5. `PagingSuppressed` child → no job enqueued and the job itself sends nothing;
6. runaway guard exhausted → no send, skipped audit row, job succeeds;
7. group incident (anchors under the group incident UID) → notice sent;
8. unlinked/unverified contact in the fallback → skipped.
