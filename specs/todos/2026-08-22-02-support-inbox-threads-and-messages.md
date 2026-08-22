---
model: opus
effort: high
---

# Human messages our bots cannot parse are silently discarded

## Problem

Every inbound channel SolidPing owns drops anything that is not a recognised
command. A person who replies to an alert — to ask a question, to report a
problem, to say the page was a false alarm — is talking to a black hole. Nobody
is notified, nothing is stored, and there is no surface anywhere in the product
where that message can be read.

This is not theoretical. On the dev instance, at 21:57 on 2026-08-21, a real
person replied to a WhatsApp alert. The entire surviving record is:

```
INFO "inbound whatsapp message received (no command handling in v1)"
     messageId="wamid.HBgLMzM2ODY5NTU0MDUVAgASGCBBQ0FCRjBGQkUwOUZENzg0..." type=text
```

The message body and the sender's number were both parsed off the webhook and
then thrown away.

### Where messages die today

| Channel | Drop point | What survives |
|---|---|---|
| WhatsApp | [`whatsappcb/handler.go:151`](server/internal/handlers/whatsappcb/handler.go#L151) `logInbound` | message id + type. **Not** the body, **not** the sender |
| Telegram (prose) | [`telegramcb/handler.go:255`](server/internal/handlers/telegramcb/handler.go#L255) `case "":` | chat id only |
| Telegram (unknown command) | [`telegramcb/handler.go:257`](server/internal/handlers/telegramcb/handler.go#L257) `default:` | chat id + command word; user gets "unknown command" |
| SMS | **no inbound route exists at all** | nothing — Twilio's Messaging Service has `use_inbound_webhook_on_number: true` but SolidPing exposes no endpoint, so an SMS reply dies at Twilio |
| Email | **no inbound path exists** | nothing (`internal/checkers/checkimap` is a *monitoring checker*, not an inbox) |

The WhatsApp struct already carries everything we need —
[`webhook.go:77`](server/internal/integrations/whatsapp/webhook.go#L77)
`InboundMessage` parses `From`, `ID`, `Timestamp`, `Type` and `Text.Body`. The
data arrives and is deliberately discarded.

### Why this costs more than it looks

An inbound WhatsApp message opens a **free 24-hour customer-service window** in
which we may reply with ordinary text instead of a pre-approved template.
Today that window opens and closes without anyone knowing it existed. The one
moment when talking to a user is both free and unrestricted is the moment we
are guaranteed to miss.

For a monitoring product this is also a trust problem: the person messaging us
is usually mid-incident.

## Proposal

Two instance-level tables, `support_threads` and `support_messages`, fed by a
small per-channel adapter, exposed to SuperAdmins as an inbox in dash0 with the
ability to reply through the originating channel.

**Capture is the invariant.** If a human sent it and the system could not act on
it, it is recorded. A capture failure must never be able to break the channel it
came from: the webhook still returns its normal 2xx and the alerting path is
untouched. Capture is best-effort *for the request*, but a failure is logged at
WARN and counted, never swallowed silently.

### Scoping — instance-level, with attribution

Threads belong to the **instance**, not to an organization, and are visible to
users with `SuperAdmin`
([`middleware/auth.go:536`](server/internal/middleware/auth.go#L536) already
provides `RequireSuperAdmin`).

This is deliberate. The sender of an inbound WhatsApp message is a phone number;
frequently there is no org to attribute it to at all, and a message from a
stranger must not be silently dropped for lack of one. Org-scoping would force a
catch-all org and make the unattributable case the broken case.

When the sender *can* be resolved — the `From` matches a verified
`user_contacts.value` for that channel type
([`models/user_contact.go`](server/internal/db/models/user_contact.go)) — record
`user_uid` and `organization_uid` on the thread as **attribution**. Attribution
is a hint for the operator, never an access-control boundary: it does not grant
the org visibility of the thread.

For a self-hoster, this is exactly right with no code path differing: their
instance's support inbox is their own.

### Schema

```sql
support_threads
  uid              varchar(36) pk
  channel          text not null        -- whatsapp | telegram | sms | email
  channel_identity text not null        -- E.164, telegram chat id, email address
  subject          text not null default ''
  status           text not null        -- open | pending | closed
  organization_uid varchar(36) null     -- attribution only, nullable
  user_uid         varchar(36) null     -- attribution only, nullable
  last_message_at  timestamptz not null
  unread_count     int  not null default 0
  created_at, updated_at, deleted_at

  unique (channel, channel_identity) where status <> 'closed'

support_messages
  uid           varchar(36) pk
  thread_uid    varchar(36) not null -> support_threads(uid) on delete cascade
  direction     text not null        -- inbound | outbound
  body          text not null
  raw_type      text not null default 'text'  -- text | image | audio | unsupported
  external_id   text null            -- provider message id; unique per channel
  author_uid    varchar(36) null     -- the SuperAdmin who sent an outbound reply
  delivery      jsonb null           -- reuses the delivery-status shape
  created_at, updated_at

  index (thread_uid, created_at)
  unique (channel_scope, external_id)  -- idempotency, see below
```

`(channel, channel_identity)` unique **only while the thread is not closed** is
what makes a reply continue an existing conversation while a new message after
closure opens a fresh thread. Without the partial predicate, a number could
never come back a second time.

`external_id` must be unique so that a webhook retry — which Meta and Twilio
both do — cannot double-insert. **Providers retry on any non-2xx**, so this is a
guaranteed occurrence, not an edge case.

### Migration

Add **`016_v0_18_0.{up,down}.sql`** in *both*
`server/internal/db/postgres/migrations/` and
`server/internal/db/sqlite/migrations/`, kept in parity (`migrationguard`, and
the `/sync-pg-to-sqlite` skill).

Do **not** append to the existing `015_v0_18_0`. It is unreleased — it is absent
from the `v0.17.0` tag — so appending looks safe, but any developer database that
has already recorded `015` will **silently skip** the appended statements and
then fail at runtime on a missing table. This repository has hit exactly that
failure before during pre-release migration consolidation. A fresh sequential
number costs nothing and cannot be skipped; two files sharing a version label is
fine.

SQLite has no partial unique index over an expression in all versions we target —
verify, and fall back to a plain unique on `(channel, channel_identity, status)`
plus application-level enforcement if needed. The two dialects must agree on
behaviour, not merely both apply.

### Capture rules per channel

| Channel | Capture when | Notes |
|---|---|---|
| WhatsApp | every inbound message | replace the body-discarding `logInbound`; non-text types record a placeholder body and `raw_type` |
| Telegram | `command == ""`, and unknown commands | known commands are unchanged; keep replying "unknown command" *and* capture it — a mistyped command is often a person trying to talk |
| SMS | new inbound webhook + route | must **not** capture `STOP`/`START`/`HELP` — those are carrier keywords with legal meaning, handled by Twilio; capturing them as support tickets would bury real opt-outs |
| Slack DM | `message` events with `channel_type == "im"` | **needs a new scope — see below.** Only DMs; channel messages and `app_mention` keep their current behaviour |
| Discord DM | `MESSAGE_CREATE` where the channel is a DM | **needs a new gateway intent — see below.** Guild messages keep their current behaviour |
| Email | out of scope for v1 — no inbound path exists | see Open questions |

#### Slack and Discord DMs are each blocked on a permission change

Both are treated as peer channels to WhatsApp/Telegram/SMS — same tables, same
inbox, same reply box. Neither works today, and the two obstacles have very
different costs:

**Slack — a scope change that forces every workspace to reinstall.** The
requested bot scopes ([service.go:121](server/internal/integrations/slack/service.go#L121))
are `chat:write`, `app_mentions:read`, … with **no `im:history`**. Without it
Slack never delivers `message.im`, so a DM to the bot is not merely ignored —
it never arrives. Adding the scope requires subscribing to the `message.im`
event *and* re-authorising: **Slack does not grant new scopes to existing
installs**, so every already-connected workspace must reinstall the app before
its DMs appear. That is a user-visible migration, not a deploy, and it needs its
own comms. Weigh it before committing — Slack DM support may deserve to trail
the other channels for that reason alone.

`handleMessage` already ignores `Subtype != "" || BotID != ""`
([events.go:258](server/internal/integrations/slack/events.go#L258)), which
correctly drops our own posts; keep that and add the `channel_type` split.

**Discord — an intent change, cheap by comparison.**
`gatewayIntents = intentGuilds | intentGuildMessages | intentMessageContent`
([gateway.go:44](server/internal/integrations/discord/gateway.go#L44)) omits
`DIRECT_MESSAGES` (`1 << 12`), so DMs are never delivered. Adding it is a
constant plus a gateway reconnect — **no user re-authorisation**. Note that
`MESSAGE_CONTENT` is already present and privileged; without it DM bodies would
arrive empty, the same silent failure the existing comment documents.

Discord will only deliver a DM if the user can open one with the bot in the
first place (shared guild / prior interaction), so DM support is a
best-effort addition rather than a guaranteed contact path.

For both, the bot must ignore **its own** messages, or an outbound reply is
re-captured as a new inbound message and the thread talks to itself.

### Replying

Outbound replies are sent through the originating channel by a per-channel
adapter behind one interface. The channels are **not** equally capable, and the
UI must tell the truth about that rather than failing at send time:

- **WhatsApp** — free-form text only inside the 24-hour window opened by the
  user's last inbound message. Outside it, a free-form reply is *impossible*;
  only an approved template may be sent. The thread must therefore show the
  window state and disable the reply box (with the reason) once it lapses. This
  is the single largest constraint in the feature.
- **Telegram** — always repliable to a known chat id. No window, no cost.
- **SMS** — always possible and **costs money per segment**; replies must pass
  the same instance-spend guards as alerting
  ([`config/sms.go`](server/internal/config/sms.go) `GlobalRunawayPerHour`,
  `AllowedCountries`) so the support inbox cannot become an unmetered spend path.
- **Email** — deferred with inbound.

An outbound reply is written as a `support_messages` row with
`direction = outbound` and `author_uid` set, and its delivery status is updated
from the same provider callbacks that already drive
`incident_notifications` — reuse that path rather than inventing a second one.

### Reply-To on outbound email, and the "you can reply" notice

Email is the one channel where support can work *before* any inbox exists: set a
`Reply-To` and a human mailbox receives the reply. This ships in the same change
because it is what makes email support real, and because it is the natural
inbound source for the deferred email capture (Open question 1).

**Instance config.** Add `ReplyTo` to `EmailConfig`
([config.go:668](server/internal/config/config.go#L668)), env
`SP_EMAIL_REPLY_TO`. Empty default — unset means no `Reply-To` header and no
notice, so nothing changes for an installation that does not want this. On our
deployments it is set to `florent@solidping.io`.

The plumbing largely exists: `Recipients.ReplyTo` is already applied at
[sender.go:149](server/internal/email/sender.go#L149). The new config value is
the **default** used when a message is eligible and sets no explicit
`ReplyTo` of its own; an explicit per-message value always wins.

**Security-critical mail must not carry it.** A `Reply-To` pointing at a human
mailbox invites the recipient to reply *to a password-reset mail* — plausibly
pasting a credential, a reset link, or a code into an email to a person. It also
undermines the "this is automated, do not engage" framing that anti-phishing
guidance leans on for exactly these messages. So they keep no `Reply-To` and no
notice.

The repo already draws this line for `List-Unsubscribe`
([email.go:20](server/internal/email/email.go#L20)): *"transactional emails
(registration, reset, invitation, password-changed)"*. **Reuse that same
partition** rather than inventing a second, divergent classification.

| Gets `Reply-To` + notice | Stays clean |
|---|---|
| `incident-created`, `incident-escalated`, `incident-resolved`, `incident-reopened`, `incident-comment`, `escalation`, `paging-nudge`, `uptime-report`, `welcome`, `test-email`, `membership_request_new`, `membership_request_decision`, `status-subscriber-update` | `password-reset`, `password-changed`, `registration`, `invitation`, `status-subscriber-confirm` |

**Fail closed.** Model this as an explicit opt-in on `Message` (e.g.
`SupportReplyable bool`), not an opt-out. Go's zero value then means *no*
`Reply-To`, so a future security email that nobody remembered to classify is
silently safe rather than silently leaking a reply path.

The cost of failing closed is that a new *notification* email silently loses the
support notice. Cover that with a **table-driven test enumerating every template
in `server/internal/email/templates/` against its expected classification**, so
adding a template fails the build until it is classified deliberately. That gets
safety and coverage at once, instead of trading one for the other.

**The notice.** Rendered in `base.html` alongside the existing footer block
([base.html:85](server/internal/email/templates/base.html#L85)), conditional on
both the instance `ReplyTo` being set *and* the message being replyable:

> ℹ️ You can reply directly to this email to reach a human — we read every reply.

This smooths the grammar of the requested copy ("contact a (human) support");
the intent — *a person, not a robot, is on the other end* — is the part that
matters, so adjust the wording freely. It must also appear in the **plain-text**
body (`Message.Text`), not only the HTML, or it vanishes for text-only clients.

Do **not** put the address itself in the body: the header carries it, and a
literal address in the text drifts the moment the config changes.

### API

Under `/api/v1/support`, SuperAdmin-gated, following the repo's REST
conventions (`{data:[…]}`, `$uid`, `PATCH`, `q`, `limit`, camelCase):

```
GET   /api/v1/support/threads?status=&channel=&q=&limit=
GET   /api/v1/support/threads/$uid
PATCH /api/v1/support/threads/$uid            { status, subject }
GET   /api/v1/support/threads/$uid/messages?limit=
POST  /api/v1/support/threads/$uid/messages   { body }   -> sends + records
```

### Dashboard — an unlinked `/support` route

A `/support` route outside `orgs/$org` (this is instance data, not org data),
**with no navigation entry anywhere**. It is reached by typing the URL. That
keeps an operator-only tool out of every customer's sidebar without building a
separate admin app.

> **A hidden route is not an access control.** Absence from the menu only
> affects discoverability; the API must still be `RequireSuperAdmin` on every
> endpoint, and the route must render "Permission Denied" for anyone else. Treat
> the URL as public knowledge.

**Thread list**, split into **active** and **expired**, plus closed. See the
note below on what "expired" means — it is *not* the same axis as `status`.

**Thread detail** reads as an ordinary chat: messages in chronological order,
inbound and outbound visually distinguished, with a reply box at the bottom —
the same mental model as the WhatsApp/Telegram/SMS thread the person is actually
sitting in. Per repo convention, start from
[`design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx)
and reuse shipped primitives; fully usable on mobile; 403 renders "Permission
Denied" and never redirects (a non-SuperAdmin hitting `/support` must not loop).

#### "Active / expired" is the reply window, not the thread status

These are two independent axes and conflating them will produce a confusing UI:

| | Meaning | Set by |
|---|---|---|
| `status` | open / pending / closed | the operator, deliberately |
| reply window | can we still send a free-form reply *right now* | the channel's rules, by the clock |

A thread can be **open but expired** (the customer's question is unanswered, yet
WhatsApp will no longer accept a free-form reply) — which is precisely the state
the operator most needs to see, because it is the one where the product cannot
do what the UI otherwise implies.

The window is **derived**, never stored as truth: compute it from the last
inbound message's timestamp and the channel's rule, so it cannot go stale.

| Channel | Window |
|---|---|
| WhatsApp | 24 h from the last inbound message — the only genuinely expiring channel |
| Telegram, Slack, Discord | never expires |
| SMS | never expires, but each reply costs money |
| Email | never expires |

When a WhatsApp thread is expired the reply box is disabled **with the reason
shown**, not left enabled to fail at send time.

### Notification — mirror every inbound message to the support mailbox

Capture without notification only moves the black hole. **Every** captured
inbound message — WhatsApp, Telegram, SMS, Slack DM, Discord DM — is mirrored as
an email to the same address configured as `SP_EMAIL_REPLY_TO`
(`florent@solidping.io` on our deployments).

That address is deliberately the single funnel: whatever channel a person picks,
it lands in one human mailbox that is already read daily, and it keeps working
before any inbox UI is opened. If `SP_EMAIL_REPLY_TO` is unset, no mirror is
sent — the feature stays off as a whole.

The mirror carries the sender, the channel, the body, and a **deep link to the
thread** in `/support`.

Three failure modes to design against, all of which are easy to ship by
accident:

1. **Do not create a mail loop.** Open question 1 proposes capturing inbound
   email by polling this very mailbox. The moment that lands, our own mirrors
   become inbound mail and get re-captured, generating another mirror, forever.
   Stamp every mirror with `Auto-Submitted: auto-generated` (RFC 3834) **and** a
   private header such as `X-SolidPing-Support-Mirror: 1`, and make the future
   email capture skip both. Writing the marker *now*, while nothing reads it, is
   what makes the later feature safe — retrofitting it means the loop ships
   first.
2. **Replying to a mirror does not reach the customer.** It goes to whatever the
   mirror's own `Reply-To` says, not to the WhatsApp number. An operator will
   assume otherwise and answer into the void. So the mirror must **say plainly
   that it is a notification, not a conversation**, and lead with the thread
   link as the way to actually reply. The mirror itself is *excluded* from the
   support `Reply-To` rule above — pointing it at the support address would make
   it reply to itself.
3. **Rate-limit it.** These are publicly reachable numbers; someone texting a
   hundred times must not produce a hundred emails. Reuse the per-thread
   throttle (one mirror per thread per N minutes, subsequent messages folded
   into the next one or summarised), and cap total mirrors per hour instance-wide.

A mirror-send failure must never fail the capture: the message is already
stored, and a bounced notification is a smaller problem than a lost message.
Log at WARN and count it.

### Abuse and privacy

These endpoints are fed by **publicly reachable phone numbers**. Treat the table
as attacker-influenced:

- cap stored `body` length (truncate, flag `truncated`), cap messages per thread
  per hour, cap new threads per identity per day;
- never render message bodies as HTML in dash0 — escape everything;
- capture failures must be counted in metrics, so a silent capture outage is
  visible.

**Message bodies are personal data.** This introduces a new category of stored
personal data and therefore requires, in the same change:

- a retention period (proposal: purge closed threads after 12 months) applied by
  the existing cleanup job pattern;
- deletion on request, wired into the existing account-deletion path;
- an update to the published
  [data-deletion page](https://www.solidping.io/legal/data-deletion) and the
  privacy policy's retention table in the `solidping-website` repo, which today
  describe **no** inbound-message store.

Shipping capture without the privacy work would put the published policy in
conflict with what the service actually does.

### Testing

- table-driven handler tests per channel: a prose message creates a thread; a
  known command does not; a repeated `external_id` does not double-insert;
- a capture failure (DB down) still returns the channel's normal 2xx and leaves
  alerting unaffected — the negative control that matters most;
- WhatsApp 24h-window logic: repliable inside, refused outside, with the refusal
  surfaced rather than a provider error;
- SMS `STOP` is *not* captured as a support message and still opts the user out;
- an **outbound reply is not re-captured** as inbound on Slack and Discord — the
  self-talking-thread bug, which only shows up once replying works;
- a Slack channel message and an `app_mention` still behave as before; only
  `channel_type == "im"` is captured;
- the mirror carries `Auto-Submitted` and `X-SolidPing-Support-Mirror`, and the
  throttle collapses a burst into one mail;
- a mirror-send failure still leaves the message captured (assert the row
  exists after forcing the mailer to fail);
- Playwright: inbox list with active/expired split, thread detail, reply, a
  disabled reply box with a reason on an expired WhatsApp thread, and the 403
  for a non-SuperAdmin on the unlinked `/support` route.

Email `Reply-To` needs its own negatives, because the failure mode is silent:

- the enumerating classification test above — every template in
  `server/internal/email/templates/` asserted against its expected
  classification, so a new template cannot be added unclassified;
- `password-reset` and `password-changed` carry **no** `Reply-To` header and
  **no** notice even when `SP_EMAIL_REPLY_TO` is set (the assertion that
  actually protects the security mail);
- with `SP_EMAIL_REPLY_TO` unset, no eligible email carries the header or the
  notice — the feature is genuinely off, not defaulted on;
- an explicit per-message `ReplyTo` overrides the instance default;
- the notice appears in the plain-text body as well as the HTML.

## Open questions

1. **Email inbound.** No support path exists, but the machinery does — and it is
   not the one this spec first assumed. `server/internal/handlers/emailcheck/`
   already consumes a mailbox over **JMAP** (`server/internal/jmap`) to drive the
   passive `email` check type, so inbound-mail ingestion is a solved problem in
   this codebase: reuse `jmap.Handler`, not new IMAP polling.

   Read `specs/todos/2026-08-22-01-email-check-inbound-dedup.md` before building
   on it — that spec documents the existing consumer minting **duplicate results
   across replicas with no claim and no idempotency**. Support capture on the
   same mechanism would inherit exactly that bug, so it should land *after* the
   dedup fix and reuse whatever claim mechanism that spec introduces, rather
   than inventing a second one.

   The `Reply-To` above makes the rest tractable: every reply converges on one
   mailbox, so capture becomes "consume that mailbox" rather than "invent an
   addressing scheme". Recommend a separate spec — and note that until it
   exists, **email support is a human mailbox, not a thread in the inbox**, a
   deliberate v1 asymmetry rather than an oversight. Whoever picks it up must
   also decide how a reply maps back to a thread (`In-Reply-To`/`References`
   headers, or a plus-addressed token) and must skip our own mirrors via the
   `X-SolidPing-Support-Mirror` marker.
2. **Does an org-facing view follow later?** The schema allows it (attribution is
   already recorded) but v1 deliberately ships only the instance inbox.
3. **Should Slack DM support ship in this change, or trail it?** It is the only
   part of the spec that imposes a **user-visible migration**: adding
   `im:history` forces every already-connected workspace to reinstall the app
   before its DMs arrive. Discord's intent change carries no such cost. Splitting
   Slack out would let everything else land without a reinstall campaign — worth
   deciding before implementation starts, not during.

## Resolved open questions

Answered by the repository owner on 2026-08-22. These are decisions, not
suggestions — implement to them.

**1. Email inbound.** *"No support path exists, but the machinery does… Recommend
a separate spec."*

**Decision: out of scope here — a separate spec, later.** Do NOT build inbound
email support capture in this change. Ship v1 with the deliberate asymmetry the
question describes: **email support is a human mailbox, not a thread in the
inbox.** Implement the `Reply-To` / `SP_EMAIL_REPLY_TO` behaviour described
above (it is what makes the future capture tractable) and the
`X-SolidPing-Support-Mirror` marker, but no JMAP consumption for support.
Rationale: capture on the JMAP path must not be built until
`2026-08-22-01-email-check-inbound-dedup` has settled its claim mechanism,
otherwise support capture inherits the duplicate-minting bug. State the
asymmetry explicitly in the user-facing docs so it reads as a decision rather
than a gap.

**2. Does an org-facing view follow later?**

**Decision: not in v1 — instance inbox only.** Keep recording attribution in the
schema so an org-facing view stays possible later, but build no org-facing
surface, no org-scoped endpoints, and no org-scoped permissions in this change.

**3. Should Slack DM support ship in this change, or trail it?**

**Decision: ship Slack DM support in this change.** Add the `im:history` scope
and handle Slack DMs alongside Discord and in-app messages — do not split it
into a follow-up. The consequence is accepted deliberately: **every
already-connected Slack workspace must reinstall the app before its DMs
arrive.** Therefore you must also:

- add `im:history` to the Slack app manifests (`wiki/slack/manifest*.json`) and
  note the required scope change in the Slack wiki page;
- make the reinstall requirement explicit in the docs-site Slack page and in the
  dash0 Slack integration UI, so an operator learns about it in-product rather
  than from a silently empty inbox;
- degrade cleanly on a workspace that has not yet reinstalled — missing
  `im:history` must not error the integration or spam logs; treat it as "DM
  capture unavailable until reinstall" and surface that state.

**Ordering.** This spec runs LAST in the current batch, after
`2026-08-22-01-email-check-inbound-dedup` has landed, per its own stated
dependency.
