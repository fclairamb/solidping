---
model: opus
effort: high
---

# Unacknowledging an incident is silent, so everyone still believes the first responder has it

## Problem

### 1. Unack tells nobody, anywhere

`unacknowledgeIncidentByOrgUID`
(`server/internal/handlers/incidents/service.go:2985`) clears
`acknowledged_at`/`acknowledged_by`, sweeps pending jobs, writes an
`incident.unacknowledged` event and publishes a realtime hint. That is all.
The silence is deliberate — spec `2026-08-24-01` recorded it as a resolved
decision ("**silent** — unack sends no notification of its own", rationale:
"rare operator action") — but the decision is wrong in the one way that
matters, and the surrounding code says so itself. The doc comment on
`queueAckNotifications` (`ack_notice.go:16-21`) argues that an ack must be
announced everywhere *because* otherwise "the four people who were woken up
have no way to learn that the fifth already picked it up". Withdrawing that
ack inverts the sentence and every word still holds: four people now believe
an incident is owned when it is not.

Three concrete failures follow from it:

- **The ack announcement is never retracted.** `queueAckNotifications`
  fanned "✅ Acknowledged by Alice" out to every channel the incident's
  alerts reached. Those messages stay on screen, unqualified, forever.
- **The in-place rewrites are worse than the fan-out.** Slack
  (`integrations/slack/interactions.go`, `chat.update` via
  `slack/client.go:170`) and Discord
  (`integrations/discord/interactions.go:297`, `bot_client.go:192`) rewrite
  the *original alert message* to show the acknowledgment. After an unack the
  incident's own alert card in the channel still reads "Acknowledged by
  Alice". This is the single most misleading artifact in the system: it is
  not a stale notification scrolled out of view, it is the incident's
  canonical message showing false state.
- **The people paged by escalation policy are stranded.**
  `queueTelegramAckNotice` (`ack_notice.go:177`, job
  `jobtypes/job_incident_ack_notice.go`) told every paged Telegram chat the
  incident was taken. Nothing tells them it was released.

There is a second, larger hole visible from the same function: **unack does
not resume escalation.** Escalation is only scheduled at incident open
(`service.go:1386` → `scheduleEscalationPolicy`); the ack cancelled the cycle
and nothing reschedules it. So the state after an unack is: not owned, not
paging, nobody told. That combination is strictly worse than never having
been able to ack. It is out of scope to fix blind, but it must be decided
here rather than discovered later — see the open question.

### 2. Comments reach channels, but not the people who were paged

Comments are *already* dispatched: `addCommentByOrgUID` calls
`queueCommentNotifications` (`service.go:2759`), and every entry point
(`AddComment`, `AddCommentFromSlack`, `AddCommentFromSlackCommand`,
`AddCommentFromDiscord`, `AddCommentFromDiscordCommand`,
`AddCommentFromTelegram`, `service.go:2577-2698`) funnels through it. The
fan-out goes to `commentFanoutConnections` — the same connection set the
alerts reached — filtered by `notifications.AcceptsEventType` (SMS/voice opt
out) and `isCommentEchoOrigin`. So the blanket claim "comments are not
dispatched" does not hold.

The real gap is narrower and asymmetric with ack:

| | check-attached channels | people paged by escalation policy |
|---|---|---|
| `incident.acknowledged` | ✅ `queueAckNotifications` | ✅ `queueTelegramAckNotice` |
| `incident.comment` | ✅ `queueCommentNotifications` | ❌ nothing |

`queueCommentNotifications`' own doc records this as intentional v1 scope
("People paged through an escalation policy are not forwarded comments",
`service.go:2846`). The consequence: someone woken by a Telegram page who
is not on any of the check's channels gets the page, gets the ack notice,
gets the resolution notice — and never sees a word of the discussion in
between.

## Proposal

Three pieces, in decreasing order of value. Piece A alone fixes the reported
harm; B and C are the completion.

### A. Retract the acknowledgment where it was asserted

On a real unack transition (past the `AcknowledgedAt == nil` early return at
`service.go:2997`), and only when the incident is still open and paging is
not suppressed — same guards as `queueAckNotifications`
(`ack_notice.go:53`):

1. **Revert the in-place rewrites first.** Slack and Discord must put the
   incident's own alert message back to its unacknowledged rendering. This is
   an edit of an existing message, not a new one, and it is the highest-value
   part of this spec. Slack has the anchors it needs (`chat.update` with the
   stored channel + ts); check whether Discord stores the message id it
   rewrote — if it does not, storing it is part of this work.
2. **Fan a notice out** to `commentFanoutConnections`, mirroring
   `queueAckNotifications` exactly: `notifications.AcceptsEventType` gating
   (add `NotifiesAcks`-equivalent handling for
   `incident.unacknowledged` in `notifications/registry.go:76` — reuse
   `NotifiesAcks`, do **not** add a third capability flag), plus an
   `isUnackEchoOrigin` skip for the surface the unack came from *only if*
   that surface's message was reverted in step 1.
3. **Tell the paged people**: a `incident_unack_notice` job modeled on
   `job_incident_ack_notice.go`, threaded onto the same anchors, reading
   rather than consuming them.
4. **Wording carries the call to action.** Not "unacknowledged" — the point
   is that the incident is unowned again. Something like
   "⚠️ Acknowledgment withdrawn by {actor} — this incident is unowned again
   and needs someone to take it." Same `AckInfo`-shaped payload
   (`notifications/ack.go`) so the actor/via resolution is reused, not
   reinvented.
5. **PagerDuty**: the Events API v2 has no un-acknowledge action —
   `event_action` accepts `trigger`/`acknowledge`/`resolve` only
   (`notifications/pagerduty.go`). Do **not** send `trigger`, which would
   create a second PD incident. Either skip PD for this event type or send a
   non-state-changing note; decide, and cover it with a test.
6. **No new configuration surface**, matching the ack decision: no
   per-destination toggle, no column, no API field. The only opt-out is the
   existing per-channel-type one.

### B. Forward comments to the people paged by the escalation policy

Close the asymmetry in the table above: comments reach paged person contacts
(Telegram today) the same way ack notices do, threaded onto the incident's
anchors, with the same per-chat marker discipline
`job_incident_ack_notice.go` uses. Rate/volume is the real risk here — a
chatty incident should not turn into thirty phone buzzes — so this piece
must state its throttling or batching rule explicitly rather than fanning out
one job per comment unconditionally.

### C. Documentation

`wiki/features/notifications-and-escalation.md` gains the ack/unack/comment
reach matrix above, and the `2026-08-24-01` decision is recorded as
superseded rather than silently contradicted.

## Honest assessment

Recorded because the request asked for it, and because it changes the shape
of the work:

- **The unack half is right, and understated.** The stated harm ("people
  think the first responder dealt with it") is real, but the sharper version
  is that the incident's *own alert card* in Slack/Discord keeps asserting
  the acknowledgment. Adding a new message while leaving that card untouched
  would be a half-fix — hence piece A.1 ahead of A.2.
- **"Rare operator action" was never a good reason for silence.** Rarity
  argues for cheapness of implementation, not for withholding the
  information. A wrong belief held by five on-call engineers costs the same
  whether it is created once a month or once a day.
- **But the "someone else shall acknowledge" premise has a hole**: unack does
  not resume escalation. Telling people "this is unowned again" while the
  system itself has stopped paging is honest but incomplete — it converts a
  silent failure into a loud one that still depends on a human noticing a
  chat message. Whether unack should reschedule the escalation cycle is the
  one genuinely open design question here.
- **The mis-click case argues for retraction, not for a page.** The documented
  use case for unack is "ack'd by mistake" (`service.go:2972`). That is an
  argument for editing the record straight and posting one channel message —
  which is what this spec proposes — and against treating unack as a fresh
  paging trigger.
- **The comment half is largely already done**, and the part that isn't was a
  deliberate scope call with a real cost on the other side (noise on people's
  phones). It is worth doing, but it is a smaller, more debatable win than
  the unack work and should not hold it up. If the two must be split, ship A
  first.

## Resolved open questions

Decided 2026-08-28. These are directives — implement exactly this, do not
re-litigate them.

**Q: Should unack resume the escalation cycle?** Options were (a) chat notice
only, (b) restart from step 1, (c) resume from the step the ack interrupted.

**Decision: (c) — resume from the step the ack interrupted.** Unack means the
incident is genuinely unowned again, so paging must continue; but it resumes
from where the acknowledgment interrupted the cycle rather than restarting at
step 1, so undoing a mis-click does not replay pages that already fired. This
supersedes the spec's own "(a) is the safe default if none is given" line —
(a) is NOT what was chosen, and the notice wording must therefore NOT say that
no further paging will happen. It must say the opposite: escalation resumes.

Implementation consequence: the escalation state at ack time has to be
recoverable, so the resumed cycle knows which step it was on. If that step
index is not currently persisted, persisting it is part of this work — say so
plainly rather than silently falling back to a restart.

**Q: Comment forwarding volume (B) — per-comment, or coalesced over a short
window?**

**Decision: per-comment, immediately.** No batching, no coalescing window. A
comment is forwarded to the people the escalation policy paged as soon as it
is posted. This accepts the notification-noise cost that the original scope
call was avoiding; that trade was made deliberately and is not a bug to fix
later.

**Q: Discord message id — is the rewritten alert message's id persisted
anywhere today? If not, A.1 needs a storage change for Discord.**

**Answer: yes, it is already persisted — no storage change is needed.**
Verified in the source: `server/internal/notifications/discord.go:32` defines
`discordKeyMessageID = "message_id"`; `:211` sets `payload.MessageID =
result.ID`; `storeThreadInfo` at `:247` writes the forward incident→message
entry, its own comment saying it is "used to edit the original embed"; and
`:317` reads `MessageID` back out of that entry, with `:332` guarding on both
`ChannelID` and `MessageID` being present. A.1 can therefore rewrite the
Discord alert card the same way it rewrites Slack's, using the existing
thread-info entry.

## Open questions

- **Should unack resume the escalation cycle?** Options: (a) no — chat notice
  only, current behavior preserved, cheapest, leaves a paging gap; (b) yes,
  restart the cycle from step 1 — closes the gap, risks a page storm on a
  mis-click undo; (c) yes, but from the step the ack interrupted. Needs a
  decision before implementation; (a) is the safe default if none is given,
  and the spec must then say plainly in the notice wording that no further
  paging will happen automatically.
- **Discord message id**: is the rewritten alert message's id persisted
  anywhere today? If not, A.1 needs a storage change for Discord (Slack is
  already covered by the incident's thread anchors).
- **Comment forwarding volume (B)**: per-comment, or coalesced over a short
  window?
