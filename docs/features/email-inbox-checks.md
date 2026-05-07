# Email-inbox passive checks

A check that succeeds when a specific email arrives at a specific
address. Inverse of every other check type — instead of SolidPing
reaching out to your service, your service (or some monitor it talks to)
sends an email to a per-check address SolidPing watches.

> **Not to be confused with:** "email" the *notification channel*. Both
> use SMTP and the word "email" but otherwise share nothing. Channels
> *send* mail to people; passive email checks *receive* mail from
> services. Different code paths, different config, different tables.

## How it works

```
external service ── SMTP ──▶ inbox provider ── JMAP ──▶ SolidPing
                                                         │
                                                         ▼
                                                  match by token
                                                         │
                                                         ▼
                                                 record check result
```

1. **You configure a JMAP-capable inbox** (Fastmail, mailbox.org,
   self-hosted Stalwart / Apache James / dovecot+stalwart, etc.) with
   one *catch-all* address pattern, e.g. `*@inbox.solidping.example.com`.
2. **You set system parameter `email.inbox`** with the JMAP session URL,
   username, password, address domain, and optional tunables. The JMAP
   manager reads this row at startup and reconnects when it changes.
3. **For each check of type `email`**, the create flow generates a
   48-hex-character secret token stored at `config.token`. The dashboard
   renders the resulting address as `<token>@<addressDomain>` (the
   domain is *not* stored on the check — it's resolved fresh from the
   system parameter at render time, so a domain change reroutes
   existing checks atomically).
4. **The JMAP manager subscribes to the inbox.** If the server supports
   EventSource (push), the manager streams; otherwise it polls every
   `pollIntervalSeconds` (default 900 = 15 min).
5. **Each new email goes through the handler chain** in the order
   handlers were registered. The `emailcheck` handler tries to match
   the token; on a hit it records a result with the resolved status and
   returns `OutcomeProcessed`, which moves the email to the `Processed`
   mailbox. On a token-shaped no-match it returns `OutcomeRejected` —
   also moves to Processed but signals "permanently invalid". On no
   token at all it returns `OutcomeIgnored` and the email moves on to
   the next handler.
6. **The result feeds the normal pipeline** — `ProcessCheckResult`
   evaluates streaks and opens / closes incidents the same way an HTTP
   check would.

## Status resolution

The handler picks a status with the following priority
([`emailcheck/handler.go:107`](../../server/internal/handlers/emailcheck/handler.go)):

1. **Plus-addressing** in the matched recipient: `<token>+down@host`,
   `<token>+timeout@host`, `<token>+error@host`. Highest priority — if
   the sender knows about the convention, this is the cleanest signal.
2. **`X-SolidPing-Status` header**. Any of `up | down | timeout | error`
   (case-insensitive).
3. **Subject tag**. Bracketed prefix, e.g. `[DOWN] backup failed`.
4. **Default `up`** — receiving the email at all is the success signal.

Any unknown status string falls through to the next priority level. If
nothing matches the email defaults to `up`.

## Mailboxes & retention

The manager owns three mailboxes by JMAP role:

| Role | Default name | Purpose |
|---|---|---|
| `Inbox` | "Inbox" | Where new mail arrives. The manager watches this. |
| Processed (custom) | "Processed" | Move-target after a handler returns Processed/Rejected. Kept for `processedRetentionDays` (default 30) for forensic trail. |
| `Trash` | provider-default | Best-effort pickup of the standard trash role. Old "Ignored-by-everyone" emails get moved here after `failedRetentionDays` (default 7). |

The cleanup loop runs alongside the watch loop; both are idempotent.
Killing the manager and restarting it is safe.

## Configuration

The config lives in `system_parameters` under the key `email.inbox`
([`jmap/manager.go:18`](../../server/internal/jmap/manager.go)) as
JSON matching the `Config` struct
([`jmap/types.go:23`](../../server/internal/jmap/types.go)):

```json
{
  "enabled": true,
  "sessionUrl": "https://api.fastmail.com/jmap/session",
  "username": "checks@yourdomain.example",
  "password": "<app password>",
  "addressDomain": "checks.yourdomain.example",
  "mailboxName": "Inbox",
  "processedMailboxName": "Processed",
  "pollIntervalSeconds": 900,
  "processedRetentionDays": 30,
  "failedRetentionDays": 7,
  "rewriteBaseUrl": ""
}
```

The `password` field is stored encrypted at rest when
`SP_ENCRYPTION_MASTER_KEY` is set — same envelope encryption that
covers check-config secrets and channel settings. The dashboard's
server-config page edits this JSON; the manager reconnects when it
changes.

`rewriteBaseUrl` is for environments where the JMAP server's discovered
URLs are unreachable from the SolidPing host (corporate proxy, NAT, dev
container). Set it to the externally-reachable base; the client
rewrites session-discovered URLs (download, upload, eventsource) onto
that base.

## Per-check setup

A check of type `email` carries a single config field:

```json
{
  "type": "email",
  "name": "Nightly backup",
  "config": {
    "token": "a1b2c3...48hex"
  }
}
```

Don't write the token by hand — the create flow generates it. The DB
has a partial index on `json_extract(config, '$.token')` filtered by
`type = 'email' AND deleted_at IS NULL`
([`migrations/003_email_token_index.up.sql`](../../server/internal/db/sqlite/migrations/003_email_token_index.up.sql))
so token lookups during inbound mail processing are O(log n).

The dashboard renders the full address (`<token>@<addressDomain>`) on
the check detail page along with copy-to-clipboard and a "rotate
token" affordance. Rotating issues a new token — old emails to the
old address fall through to no-match (`OutcomeRejected`) and don't
record anything.

## Status & operations

The manager exposes a status snapshot via the admin API:

- `enabled`: whether the system parameter says we should be running.
- `connected`: whether the JMAP client currently has a working session.
- `mode`: `push` (EventSource active) or `poll` (fallback).
- `lastSyncedAt`: timestamp of the most recent successful sync.
- `lastError`: human-readable last error from connect/sync.
- `addressDomain`, `accountId`: passthrough for display.

This drives the "Email inbox" tile on the dashboard's server-config
page. A `connected: false` for more than a few minutes is the on-call
signal that something needs attention — either the JMAP credentials
expired or the provider rate-limited us.

## What this design buys

- **Heartbeat semantics from any service**: cron jobs, scheduled
  backups, batch pipelines. No HTTP server to expose, no auth tokens
  to manage on the sender side — just an SMTP send to a known address.
- **No webhook receiver to operate**. The IMAP/JMAP provider is
  doing the heavy lifting; SolidPing only consumes.
- **Plus-addressed status**: `+down` / `+timeout` / `+error` in the
  recipient encodes the result without any extra header rule on the
  sender side. Useful for senders that can't customize headers (think
  consumer-grade backup tools).

## What this design does NOT buy

- **Replies / threading**. The handler returns `OutcomeProcessed` and
  the email moves on. There is no auto-reply, no threading of related
  alerts.
- **Multi-account JMAP**. One inbox per deployment. Multiple orgs
  share the same JMAP manager and address domain; the per-check token
  is the per-tenant boundary.
- **Anti-spoofing**. There's no SPF/DKIM verification on the SolidPing
  side. The token is the secret — leak it and a stranger can record
  results for that check. If your service emails through a known relay,
  add a procmail rule on the inbox provider's side that rejects mail
  not from that relay.

## Where to look in the code

| Concern | File |
|---|---|
| Per-check config | [`server/internal/checkers/checkemail/config.go`](../../server/internal/checkers/checkemail/config.go) |
| JMAP supervisor (connect, watch, retry) | [`server/internal/jmap/manager.go`](../../server/internal/jmap/manager.go) |
| Token-lookup handler | [`server/internal/handlers/emailcheck/handler.go`](../../server/internal/handlers/emailcheck/handler.go) |
| Token DB index | [`server/internal/db/sqlite/migrations/003_email_token_index.up.sql`](../../server/internal/db/sqlite/migrations/003_email_token_index.up.sql) |
| EventSource (push) client | [`server/internal/jmap/eventsource.go`](../../server/internal/jmap/eventsource.go) |

## Origin

Shipped end of April 2026 across three specs:
- [`2026-04-29-01-email-inbox-jmap.md`](../../specs/done/2026/04/2026-04-29-01-email-inbox-jmap.md) — JMAP supervisor, system parameter, mailbox handling.
- [`2026-04-29-02-email-passive-checks.md`](../../specs/done/2026/04/2026-04-29-02-email-passive-checks.md) — check type, token format, status resolution priority.
- [`2026-04-29-03-email-check-frontend.md`](../../specs/done/2026/04/2026-04-29-03-email-check-frontend.md) — dashboard UI for token/address display and rotation.
- [`2026-05-02-05-email-inbox-config-display-and-live-sync.md`](../../specs/done/2026/05/2026-05-02-05-email-inbox-config-display-and-live-sync.md) — status panel and live-sync-on-config-change.
- [`2026-05-02-11-jmap-eventsource-url-template.md`](../../specs/done/2026/05/2026-05-02-11-jmap-eventsource-url-template.md) — `rewriteBaseUrl` support for proxied JMAP servers.
