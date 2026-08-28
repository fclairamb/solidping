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

## Implementation Plan

Authoritative source for this plan is `## Resolved open questions`, not the older
`## Proposal` text. Where they disagree the Resolved section wins: escalation
**resumes from the interrupted step** (not "chat notice only"), and comments are
forwarded **per comment, immediately** (no batching, no coalescing window).

### 0. What unack can be triggered from (verified)

`POST /orgs/:org/incidents/:uid/unack` (dash0, CLI, API) is the ONLY entry
point — there is no Slack/Discord "unacknowledge" button. So `via` is always
`web`, and there is no echo-origin surface to skip. `isUnackEchoOrigin` from
A.2 is therefore deliberately NOT implemented: it would be dead code guarding a
case that cannot arise, and the surface that would have needed it (a chat
unack) does not exist. If one is ever added, it must ship with the skip.

### 1. Escalation resume (the part the spec does not spell out)

Ack cancels pending escalation-step jobs via `jobsvc.CancelPendingForIncident`,
which **soft-deletes** them (`deleted_at`, status stays `pending`). Those rows
are the record of exactly where the cycle was interrupted: config carries
`StepUID`, `RepeatIndex`, `IsLastStep`, and the row carries the `scheduled_at`
the step was due at.

So the state does not need a new column — it is already persisted, it was just
never read back. New `jobsvc` method:

```go
ListForIncident(ctx, incidentUID, jobType string) ([]*models.Job, error)
```

returning **every** job of one type for the incident — all statuses,
soft-deleted rows included — oldest `scheduled_at` first.

On unack, AFTER the `cancelPendingNotifications` sweep (call order is
load-bearing, same as the ack notice — a resume enqueued before the sweep would
cancel itself), `resumableEscalationSteps` groups that history into rungs keyed
by `(stepUid, repeatIndex)` and skips a rung when either:

1. **some generation of it already ran** — any row that left `pending`
   (success, failed, retried, running): the page went out; or
2. **some generation of it is still live** — queued and not canceled, so
   re-creating it would double-page.

Surviving rungs are re-created from their **newest canceled generation**, with
its **original config verbatim**, shifted as a block by
`shift = max(0, now - earliest remaining due time)`.

That is what "resume from the step the ack interrupted" means concretely: a
rung that already paged is never recreated; the remaining rungs keep their
relative spacing; a rung overdue at unack time fires immediately, one still in
the future keeps its remaining wait. `RepeatIndex`/`IsLastStep` ride along
untouched, so the repeat chain continues from the cycle the ack interrupted
rather than from cycle 0.

**Why the correlation must span generations** (and why the simpler "resume the
canceled-pending rows" is wrong): after one ack → unack cycle a rung exists as a
canceled generation 1 *and* a live generation 2. Once generation 2 fires,
generation 1 is still canceled-and-pending, so a canceled-rows-only resume
resurrects it on the next unack — and with its due time now in the past it
fires immediately. That is the option (b) page storm this decision exists to
avoid. Two ack/unack cycles with one step firing in between reproduce it.

If the canceled set is empty (no policy, or the cycle had already run out)
nothing is scheduled and a debug line says so — unack never *starts* an
escalation that was not running.

### 2. A.1 — revert the in-place rewrites

New notifications event type `incident.unacknowledged`.

- **Slack** (`notifications/slack.go`): `handleIncidentUnack`, modeled on
  `handleIncidentReopen` — `chat.update` the stored `message_id` back to an
  active, unowned rendering (`buildUnackUpdateMessage`, with the Acknowledge
  button restored) and post `buildUnackThreadReply` under the stored thread.
  `requiresExistingThread` includes unack: without the incident's own alert in
  the channel a bare "acknowledgment withdrawn" is a context-free orphan and
  would claim the incident's thread mapping.
- **Discord** (`notifications/discord.go`): `sendFollowUp` gains an
  `editOriginal(buildUnackUpdateMessage)` case, reusing the **existing**
  thread-info entry (`discordKeyMessageID`) — no storage change, per the
  resolved question.

### 3. A.2 — fan-out

`registry.go AcceptsEventType` maps `incident.unacknowledged` onto the existing
`NotifiesAcks` flag (no third capability flag). `queueUnackNotifications` in
`ack_notice.go` mirrors `queueAckNotifications` exactly, including the
`PagingSuppressed || resolved` guard, and reuses `AckInfo` for attribution.
Every sender that renders `incident.acknowledged` explicitly gains the unack
counterpart so no channel falls back to a bare "Incident update".

### 4. A.3 — tell the paged people

New job type `incident_unack_notice`, and (piece B) `incident_comment_notice`,
both implemented over ONE shared Telegram person-notice traversal
(`job_incident_person_notice.go`) modeled on `job_incident_ack_notice.go`:
thread anchors read-not-consumed, audit-row fallback, per-chat marker, runaway
guard, per-chat failure policy.

Marker discipline differs by kind, on purpose:
- unack: one marker per incident per chat (`telegram_unacked:<incident>:<chat>`)
  and **nothing ever clears it** — the FIRST withdrawal on an incident is
  announced to a given chat and later ones are not. That mirrors the ack
  notice's own incident-scoped marker exactly (an ack → unack → re-ack cycle is
  likewise announced once), which is why it is left as-is rather than given a
  per-transition key. Channels are unaffected: they go through notification
  jobs, which carry no marker, so every unack reaches them;
- comment: one marker per **comment** per chat
  (`telegram_commented:<incident>:<commentEventUid>:<chat>`) — per-comment is
  the point, so an incident-scoped marker would suppress every comment after
  the first.

### 5. A.4 — wording

`notifications/unack.go`, reusing `AckInfo`:

> ⚠️ Acknowledgment withdrawn by {actor} — this incident is unowned again and
> escalation resumes.

The second clause is required by the resolved decision. Wording that implies
paging has stopped is wrong.

### 6. A.5 — PagerDuty

Events API v2 has no un-acknowledge; `event_action` is
trigger/acknowledge/resolve only, and `PagerDutySender.Send`'s **default branch
is `trigger`** — so leaving unack unhandled would page PagerDuty again. Unack
therefore joins the explicit "send nothing" branch alongside
`incident.escalated` / `incident.comment`, with a test asserting zero HTTP
calls. Skipping is the honest option: the only API that could move a PD
incident back to `triggered` is REST v2, and this integration holds a routing
key, not an API token.

### 7. B — comments to paged people

`queueCommentNotifications` gains a `queueCommentPersonNotice` call: one
`incident_comment_notice` job per comment, immediately, no throttle. Gated on
`PagingSuppressed` (never paged) and on `resolved` (the all-clear already
closed that conversation).

### 8. C — documentation

`wiki/features/notifications-and-escalation.md` gains the ack/unack/comment
reach matrix, and decision `2026-08-24-01` ("unack is silent") is recorded as
**superseded** with the reason.

### Tests

- unack fans out; skipped when `PagingSuppressed`; skipped when resolved;
- Slack + Discord alert messages reverted in place (message id, active
  rendering, ack button back);
- escalation resumes from the **interrupted step** — asserts the recreated
  job's `stepUid`/`repeatIndex` are the interrupted ones, not step 1 / cycle 0;
- a rung whose LATER generation already fired is never replayed across a second
  ack/unack cycle, with a positive control that the never-fired rung still is;
- a rung that is still live is not scheduled a second time;
- comments forward per comment (two comments → two person-notice jobs);
- PagerDuty sends nothing for unack.
