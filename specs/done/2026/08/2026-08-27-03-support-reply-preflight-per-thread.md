---
model: opus
effort: high
---

# The support inbox offers a reply box for threads it cannot possibly answer

## Problem

`canReply` is a **channel-level** answer to a **per-thread** question.

```go
// server/internal/handlers/supportinbox/handler.go:100
CanReply: h.svc.CanReply(thread.Channel),
```

```go
// server/internal/support/service.go:173
func (s *Service) CanReply(channel string) bool {
    _, ok := s.repliers[channel]
    return ok
}
```

`repliers` is populated once at boot (`server/internal/app/support_wiring.go`,
`registerSupportRepliers`) — for Slack, merely because `slackService != nil`.
So every Slack thread reports `canReply: true`, regardless of whether *that
thread's workspace* is reachable.

The adapters themselves already know better. `slackReplier` needs
`thread.ChannelContext["teamId"]` to resolve to a stored connection holding a
bot token; `discordReplier` needs a `channelId`; `smsReplier` needs an SMS
resolution and returns `errNoReplyRoute` without one. All three can fail for
reasons that are knowable *before* the operator types — but nothing asks them.

The result is the worst possible ordering: the UI renders an enabled reply
box, the operator writes an answer, `Reply` runs the adapter, the adapter
fails, and the failure is recorded as a stored outbound message flagged
`Delivery failed` (`server/internal/support/service.go:537`). The reply text
is kept but never sent, there is no resend, and the operator's only recourse
is to notice the small red line under their own message.

### Observed in the field

A Slack DM from workspace `T0ACME0001` was captured correctly — the thread
exists, the inbound message is there. Both operator replies failed:

```json
{"error": "connection not found", "status": "failed"}
```

`GetConnectionByTeamID` (`server/internal/integrations/slack/service.go:273`)
found no `integrations` row for that team at all — the org behind it has zero
integrations. That workspace has the bot installed on Slack's side (which is
why its `message.im` events arrive) but was never installed *through*
SolidPing's OAuth callback, so no bot token was ever stored. Capture needs no
connection — the Events API request authenticates with the instance-level
signing secret — but replying does. **Inbound and outbound have different
prerequisites, and only inbound's are checked.**

This is not exotic: any workspace whose app was installed from the Slack app
dashboard rather than through `/integrations/slack/install`, and any workspace
whose connection was later deleted, lands in exactly this state — able to
open support threads, impossible to answer.

## Proposal

1. **Make `CanReply` per-thread.** Replace the channel-keyed lookup with a
   routing pre-flight the adapter owns: each registered replier gains a
   `CanRoute(ctx, thread) (bool, reason string)` companion (or `ReplyFunc`
   grows a dry-run mode — pick one and say why in Decisions). Slack resolves
   the connection, Discord checks the channel id, SMS runs its resolution.
   The handler returns `canReply:false` **with the reason**, and the reply
   box renders disabled with that reason — the same treatment email already
   gets ("email replies are sent from the support mailbox, not from here",
   `server/internal/db/models/support.go:162`), which proves the UI surface
   for this already exists.
2. **Refuse at the API too, not only in the UI.** `Reply`
   (`server/internal/support/service.go:499`) must run the same pre-flight
   before calling the adapter and return a typed error — alongside
   `ErrNoReplier` and `ErrReplyWindowClosed` — so a stale dashboard tab, a
   scripted caller, or a race cannot store an outbound message that had no
   route. **A message with no route must not be stored as a failed send**:
   nothing was attempted, so there is nothing to record.
3. **Keep recording genuine send failures.** The distinction is
   deliberate and must be tested both ways: *unroutable* (knowable in
   advance → refuse, store nothing) versus *send failed* (attempted, the
   provider rejected it → store with `Delivery failed`, as today). The
   comment at `service.go:536` explaining why failures are stored stays
   true for the second case.
4. **Give the operator a way out.** A reply that failed for a transient
   reason is currently dead text. Add a resend on a failed outbound message
   which re-runs the pre-flight and the send. If resend is judged out of
   scope, say so explicitly in Decisions — the current state, where the
   operator's words are visibly stored and permanently unsent, is not a
   resting place.
5. **Tests proving the negative:**
   - a Slack thread whose `teamId` has no stored connection reports
     `canReply:false` with a reason, and `POST …/messages` on it is refused
     **and stores no message**; positive control: a thread with a live
     connection replies and stores a `sent` message;
   - the same pair for Discord (missing `channelId`) and SMS (no SMS
     resolution / instance not configured);
   - a routable thread whose provider call fails at send time still stores
     the message with `Delivery failed` — the behavior at point 3 that must
     survive;
   - a WhatsApp thread outside its 24h window still refuses via the existing
     window path, unchanged.

## Secondary, same surface

Slack threads are titled with a raw user id — `U0ACME1234: hi` — because
`captureDirectMessage` (`server/internal/integrations/slack/events.go:481`)
stores `msg.User` and nothing else. The Telegram capture resolves a handle
and produces a readable subject. Resolving the Slack display name at capture
time (the workspace connection, when one exists, can call `users.info`) would
make the inbox list legible. Strictly cosmetic — do it only if it does not
complicate the pre-flight work above, and note that it depends on the very
connection whose absence causes the primary bug.

## Decisions

- **Pre-flight must not call the provider.** It resolves local routing state
  (is there a connection, a channel id, an SMS route) and nothing more. A
  liveness probe per thread render would put a provider round-trip on the
  inbox list, and a provider that is merely slow would start reading as
  "cannot reply".
- **A missing route is not an error state for the thread.** The thread stays
  open and readable; only the reply box is disabled. A person who messaged us
  is still owed a record of it even when we cannot answer in-channel.

- **`CanRoute` companion, not a dry-run flag on `ReplyFunc`.** The pre-flight is
  a separate registered function — `RouteFunc func(ctx, *SupportThread)
  ReplyRoute` — sitting next to the channel's `ReplyFunc`, and adapters register
  the pair. Chosen over a `dryRun bool` on `ReplyFunc` because:
  1. **The type makes sending impossible.** A `RouteFunc` returns
     `(canReply, reason)`, not a provider message id — there is nowhere for a
     send to put its result. A dry-run flag leaves the send call one missed
     `if` away in every adapter, and the failure mode of that miss is "the
     inbox list posted a message to a customer".
  2. **The reason is operator-facing text, and `ReplyFunc` has nowhere to put
     it.** Threading it through the `(string, error)` return would mean
     encoding an explanation in an error string and parsing it back out.
  3. **It is opt-in per adapter.** `RegisterReplier` keeps its old meaning
     (registered ⇒ routable), and `RegisterRoutedReplier` adds the pre-flight.
     WhatsApp and Telegram genuinely have nothing per-thread to resolve — they
     are gated by instance config at registration — so they stay on the
     two-argument form instead of growing a stub that always returns true.
  The cost is one more function per adapter that could drift from the send
  path; the tests pin them together by driving both against the same threads.

- **Resend is IN scope, and implemented.** The spec's own words —
  "the current state, where the operator's words are visibly stored and
  permanently unsent, is not a resting place" — are the argument, and the
  pre-flight makes resend cheap rather than adding a second risky path: it is
  window-check → route-check → send → rewrite `delivery`, the same three steps
  `Reply` runs, on a row that already exists. Shipping the pre-flight without
  it would also make the product *worse* in one specific way: refusing an
  unroutable reply up front means the operator can no longer even keep their
  draft as a failed row, so every failed send left behind by the old behaviour
  would become permanently unrecoverable text. `POST
  /api/v1/support/threads/:uid/messages/:messageUid/resend` re-runs the
  pre-flight and the send on a **failed outbound** message only, and rewrites
  that row's delivery rather than appending a duplicate.

## Out of scope

- Fixing the specific dev-deployment workspace, which is an install, not a
  code change.
- The stale `organization_providers` link that bricks the install flow —
  spec 2026-08-27-02. Related only in that both make a Slack workspace
  unreachable; the fixes do not overlap.

- **The secondary cosmetic item (Slack display names) is NOT done, deliberately.**
  Resolving `U0ACME1234` to a handle means a `users.info` round-trip, and the
  only place the label is used is the subject line of a thread being *created*.
  Three things make that a bad trade here:
  1. **It puts a provider call on the capture hot path.** `HandleEvents`
     dispatches synchronously and answers Slack in the same request; capture is
     the invariant this package is built around ("a capture failure must never
     break the channel it came from"), and adding a network hop to it for a
     nicer title inverts that priority.
  2. **It would leak real network calls into existing tests.**
     `support_dm_test.go` drives `DispatchEvent` against a service whose
     `newAPIClient` is not faked, so every captured DM would try to reach
     api.slack.com. Fixing that means reworking a test harness in an unrelated
     package for a cosmetic gain.
  3. **It depends on the very connection whose absence causes the primary bug** —
     the spec says so itself. The workspaces with the ugliest subjects are
     exactly the ones where `users.info` cannot be called at all.
  The clean version is a lazy `SenderLabel` resolved only when a thread is
  actually created; that is a small spec of its own, not a rider on this one.

## Implementation Plan

1. **`server/internal/support/service.go` — the pre-flight primitive.**
   - `ReplyRoute{CanReply bool; Reason string}` and
     `RouteFunc func(ctx, *models.SupportThread) ReplyRoute`.
   - `repliers` becomes `map[string]replier{send, route}`; `RegisterReplier`
     (send only) and `RegisterRoutedReplier` (send + pre-flight).
   - `CanReply(channel string) bool` is **deleted**, so no caller can keep
     asking the channel-level question by accident. It is replaced by
     `ReplyRouteFor(ctx, thread)` and a batch `ReplyRoutes(ctx, threads)` that
     memoizes on `channel + orgUid + channelContext`, so a 500-thread inbox
     does not run 500 identical connection lookups.
   - New typed error `ErrNoReplyRoute`, alongside `ErrNoReplier` and
     `ErrReplyWindowClosed`.
   - `Reply` runs window-check → route-check **before** the adapter, and
     returns `ErrNoReplyRoute` with the reason. Nothing is inserted on that
     path — no message, no thread touch.
   - `Resend(ctx, threadUid, messageUid)` re-runs the same pre-flight for a
     failed outbound message and rewrites its `delivery`. New errors
     `ErrMessageNotFound`, `ErrNotResendable`.

2. **`server/internal/app/support_wiring.go` — adapters answer the question.**
   - `slackReplyRoute`: `teamId` + `channelId` present, and
     `GetConnectionByTeamID` resolves. This is the field failure.
   - `discordReplyRoute`: `channelId` present, bot token configured.
   - `smsReplyRoute`: SMS service present, `Resolve` succeeds,
     `SMSAvailable()`. The spend guard stays on the send path — a pre-flight
     must never consume a reservation.
   - WhatsApp/Telegram stay on `RegisterReplier`: config-gated at
     registration, nothing per-thread to resolve.

3. **`server/internal/handlers/supportinbox/handler.go` — surface it.**
   - `ThreadResponse.CanReplyReason` next to `CanReply`; list uses the batch
     helper, single-thread reads use `ReplyRouteFor`.
   - `ErrNoReplyRoute` → `409 CONFLICT` with the reason.
   - `POST /threads/:uid/messages/:messageUid/resend` → 200 / 202 / 409 / 404.

4. **`server/internal/app/openapi/openapi.yaml`** — `canReplyReason`, the
   reworded `canReply`, and the resend operation. The Go client is regenerated
   once per batch by the coordinator, not here.

5. **dash0** — `canReplyReason` on the `SupportThread` type, a
   `useResendSupportReply` mutation, the reply box rendering the server's
   reason, and a Resend action on a failed outbound bubble. Locale keys in
   `en/fr/de/es`.

6. **Tests.** Slack / Discord / SMS each get a refusal + positive-control pair
   asserting `ReplyRouteFor`, the `Reply` refusal, and **that the store is
   empty afterwards**; a routable adapter that fails at send time still stores
   `Delivery failed`; the WhatsApp window path is unchanged; resend is proven
   both ways. Playwright covers the disabled box with the server reason and the
   resend button.
