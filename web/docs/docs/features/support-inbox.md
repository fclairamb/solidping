---
sidebar_position: 20
title: Support inbox
---

# Support inbox

Every inbound channel SolidPing owns used to drop anything that was not a
recognised command. A person who replied to an alert — to ask a question, to
report a problem, to say the page was a false alarm — was talking to a black
hole: nobody was notified, nothing was stored, and there was no surface anywhere
in the product where that message could be read.

The support inbox is where that traffic lands instead.

**Capture is the invariant.** If a human sent it and the system could not act on
it, it is recorded. A capture failure can never break the channel it came from:
the webhook still returns its normal `2xx` and the alerting path is untouched.
Failures are logged at `WARN` and counted, never swallowed silently.

## Where it lives

`/dash0/support` — an **unlinked** route. There is no navigation entry anywhere;
you reach it by typing the URL. That keeps an operator-only tool out of every
customer's sidebar without a separate admin app.

A hidden route is not an access control. Every endpoint under
`/api/v1/support` requires **super admin**, and the page renders "Permission
Denied" for anyone else. Treat the URL as public knowledge.

## What gets captured

| Channel | Captured | Notes |
|---|---|---|
| WhatsApp | Every inbound message | Non-text messages record a placeholder body and the real kind. |
| Telegram | Prose, and unrecognised commands | Known commands run as before and are **not** captured. A mistyped command still gets the "unknown command" answer *and* is recorded — it is very often a person trying to talk. |
| SMS | Inbound replies | `STOP`, `START`, `HELP` and the other carrier keywords are **never** captured: they carry legal meaning, Twilio's Advanced Opt-Out handles them, and filing them as tickets would bury a real opt-out. |
| Slack | Direct messages to the bot | Requires the `im:history` scope — see the [reinstall note](../configuration/notifications.md#direct-messages-and-the-support-inbox). Channel messages and `@SolidPing` mentions are unchanged. |
| Discord | Direct messages to the bot | Needs the `DIRECT_MESSAGES` gateway intent, which requires no re-authorisation. Discord only delivers a DM if the user can open one with the bot (shared server or prior interaction), so this is best-effort. |
| Email | **Not captured** | See below. |

The bot never captures its own messages. Without that, an outbound reply comes
straight back in as a new inbound message and the thread talks to itself.

### Email is a mailbox, not a thread — deliberately

Email support in this version is a **human mailbox, not a thread in the inbox**.
That is a decision, not a gap.

What ships is the half that makes email support real today: alert and
notification emails carry a `Reply-To` pointing at your support mailbox plus a
short "you can reply to this email" notice, so a reply reaches a person. What
does *not* ship is consuming that mailbox back into threads; that is a separate
change, deliberately sequenced after the inbound-email deduplication work it
would otherwise inherit bugs from.

Every mirror notification is already stamped with `Auto-Submitted:
auto-generated` and a private `X-SolidPing-Support-Mirror: 1` header, so when
inbound email capture does arrive it can skip our own mail instead of
re-capturing it into an endless loop.

## Configuration

One setting turns the whole thing on:

| Setting | Env | Parameter |
|---|---|---|
| Support mailbox | `SP_EMAIL_REPLY_TO` | `email.reply_to` |

Unset (the default) means: no `Reply-To` header, no reply notice, and **no
mirror notifications**. Capture still happens — the inbox works — but nothing is
emailed anywhere. Nothing changes for an installation that does not want this.

### The `Reply-To` never goes on security mail

Password reset, password changed, registration, invitation and status-page
subscription confirmations carry **no** `Reply-To` and no notice, even when the
support mailbox is configured.

A `Reply-To` pointing at a human mailbox invites the recipient to reply to a
password-reset mail — plausibly pasting a credential, a reset link or a code into
an email to a person — and undermines the "this is automated, do not engage"
framing anti-phishing guidance leans on for exactly these messages.

The classification is an explicit opt-in per template, so a template nobody
classified is silently *safe* rather than silently leaking a reply path, and the
build fails if a new template is added without a decision.

## Notifications

Every captured message is mirrored as an email to the same support mailbox, so
whatever channel a person picks lands in one inbox that is already read daily.

The mirror says plainly that it is a notification, not a conversation, and leads
with a link to the thread: **replying to a mirror does not reach the customer**,
it goes wherever the mirror's own `Reply-To` says.

Mirrors are rate-limited — one per thread per ten minutes, with the rest folded
into the next one, plus an instance-wide hourly ceiling. These are publicly
reachable numbers; someone texting a hundred times must not produce a hundred
emails. A mirror that fails to send never fails the capture: the message is
already stored, and a bounced notification is a smaller problem than a lost one.

## Replying

Replies go back through the channel the message arrived on. The channels are not
equally capable, and the inbox tells you the truth about that rather than failing
at send time:

| Channel | Reply window |
|---|---|
| WhatsApp | **24 hours** from the last inbound message. Outside it a free-form reply is impossible — only an approved template may be sent — so the reply box is disabled with the reason shown. |
| Telegram, Slack, Discord | Never expires. |
| SMS | Never expires, but **every reply costs money**. Replies pass the same instance spend guards as alerting (country allow-list, global runaway-per-hour), so the inbox cannot become an unmetered spend path. |
| Email | Answer from your mail client — there is no reply box for email threads. |

The window is **derived** from the last inbound message and the channel's rule,
never stored, so it cannot go stale.

### "Active" and "expired" are not the same as the status

Two independent axes, and conflating them is confusing:

| | Meaning | Set by |
|---|---|---|
| Status (open / pending / closed) | Where the operator has put it | You, deliberately |
| Reply window | Can we send a free-form reply *right now* | The channel's rules, by the clock |

A thread can be **open but expired** — the customer's question is unanswered and
WhatsApp will no longer accept a free-form reply. That is the state you most need
to see, so the inbox gives it its own section.

## Attribution

When the sender matches a **verified** contact, the thread records which user and
organization that is.

Attribution is a hint for the operator, never an access-control boundary. It does
not give that organization visibility of the thread, and there is no org-facing
view: the inbox is instance-level only. Most senders are a bare phone number with
no organization to attribute at all, and a message from a stranger must never be
dropped for lack of one.

Deleting an organization strips attribution from its threads and leaves the
conversations standing.

## Privacy and retention

Message bodies are **personal data**.

- **Retention.** Closed threads and their messages are purged after 12 months
  (`SP_SUPPORT_RETENTION_DAYS` / `support.retention_days`). Set it to `0` to keep
  everything — a supported choice for an operator under legal hold.
- **Erasure.** Deleting an organization detaches attribution immediately; closing
  a thread schedules its content for purge at the retention horizon.
- **Abuse limits.** Bodies are capped and truncated rather than rejected, with
  ceilings on messages per thread per hour and new threads per identity per day.
  Bodies are always rendered as text, never as HTML.

If you self-host and publish a privacy policy, add this store to its retention
table: capture without the corresponding policy text puts what you publish in
conflict with what your service does.

## Metrics

| Metric | Meaning |
|---|---|
| `solidping_support_capture_total{channel,outcome}` | Capture attempts by outcome (`captured`, `deduplicated`, `throttled`, `failed`). A silent capture outage is visible here. |
| `solidping_support_mirror_total{outcome}` | Mirror notifications (`sent`, `folded`, `throttled`, `failed`, `disabled`). |
| `solidping_support_dm_unavailable{channel}` | Connected integrations whose DM capture needs a reinstall to work. |
