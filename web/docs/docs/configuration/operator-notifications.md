---
sidebar_position: 12
title: Operator Notifications
---

# Operator Notifications

Operator notifications tell chosen **super admins** about instance-level
events — a customer writing into the [support inbox](../features/support-inbox.md),
or a new user signing up — through the notification routes those admins already
set up for themselves.

They exist because both of those events used to reach nobody in real time. An
inbound support message only produced an email to the instance support mailbox
(`SP_EMAIL_REPLY_TO`); if the person on duty lives in Telegram or Slack instead,
a customer's question sat unanswered until somebody happened to open the inbox.
A signup produced nothing at all beyond an optional analytics event.

## What gets delivered, and where

| Event | Fires when | Carries |
|---|---|---|
| `support.message` | An **inbound** support message is captured, on any channel (WhatsApp, Telegram, SMS, Slack, Discord) | Channel, sender, whether the thread is new, a ~200-character preview, and a deep link to the thread |
| `user.registered` | A new account is created, by any method — password, Google, GitHub, GitLab, Microsoft, Discord, Slack, OIDC, SAML, LDAP, or accepting an invitation | Email, display name, signup method, and the organization they landed in (or "no organization") |

Outbound replies do **not** fire `support.message`: a colleague answering is not
something anyone needs to be paged for.

Delivery goes out over each recipient's own **enabled notification routes**
(Account → Notifications), across every organization they belong to, in position
order:

- **email** — sent as a normal email job, so it inherits the SMTP configuration
  and the retry chain every other mail uses;
- **Telegram** — a DM from the instance bot;
- **Slack** — a DM through that organization's Slack connection;
- **web push** — the subject as the title, the first content line as the body;
- **SMS** — a compact form truncated to ~300 characters, verified numbers only.

WhatsApp routes are **skipped**: Meta does not carry free-form business text
outside an active session. The skip is logged, never silent.

A recipient who belongs to two organizations with the same email address gets
**one** notice, not two — destinations are de-duplicated by contact type and
value.

## Setting it up

Super admins configure it at **Server → Notifications**
(`/dash0/orgs/<org>/server/notifications`):

1. Turn on the **Enable operator notifications** switch.
2. Tick the events each super admin should hear about.
3. Press **Send me a test**. This delivers a real `test` notice to *you*, over
   the real transport, and reports how many routes carried it.

The table also shows each candidate's routes. A recipient with **no enabled
notification route** is flagged in amber — subscribing them would look like it
worked and deliver nothing, which is the most likely way this feature fails
quietly.

### Only super admins can be recipients

A support notice can quote a customer's message, and support threads are
super-admin-only. So a recipient must be a super admin **at delivery time**, not
merely when the configuration was saved: someone who has since lost the flag is
skipped with a warning naming them, and the dashboard keeps showing their row so
the stale subscription is visible rather than mysterious.

## Configuration storage

The configuration is the `operator_notifications` **system parameter**, editable
live — adding an operator never needs a redeploy. Its shape:

```json
{
  "enabled": true,
  "recipients": [
    { "userUid": "…", "events": ["support.message", "user.registered"] }
  ]
}
```

Unknown event names, blank or duplicate user ids, recipients subscribed to
nothing, and non-super-admin recipients are all rejected on write with
`VALIDATION_ERROR` — through the dedicated endpoints *and* through the raw
`/system/parameters` CRUD.

## Anti-burst behavior

A hundred messages in a minute must not become a hundred pushes. Support
notices reuse the mail mirror's posture:

- **one notice per thread per 10-minute fold window** — the next one says
  "…and N more message(s) in this thread";
- **an instance-wide hourly ceiling**, so a hundred different numbers texting
  once each cannot flood either.

## Observability

Every delivery attempt is counted in
`solidping_operator_notice_total{event, contact_type, outcome}`, with `outcome`
one of `sent`, `failed`, `skipped` or `enqueued`. A recipient nobody could reach
is logged at WARN **by name**. Nothing about this path drops silently.

Turning the feature off stops delivery only — the events themselves keep
happening and stay in the logs and in their own metrics.

## Relationship to the platform watchdog

The [platform watchdog](../features/observability.md) uses the same delivery transport but
keeps its own recipient list in the `platform_watchdog` parameter. It reports on
the platform's own vitals rather than on customer content, so its recipients are
not filtered on super-admin status.
