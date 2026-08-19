---
model: sonnet
effort: high
---

# SMTP check can send a real probe email, delivered to a paired passive email check, to verify the full transmission loop

## Problem

The SMTP check stops at the handshake. `Execute`
(`server/internal/checkers/checksmtp/checker.go:98`) dials, reads the
greeting, sends EHLO (`:364`), optionally upgrades with STARTTLS (`:412`) and
authenticates (`:330`) — then disconnects. It never issues `MAIL FROM`, so it
proves reachability and credentials but not the thing people actually monitor
an SMTP server for: **that mail submitted to it gets delivered**. A server
with a wedged queue, a broken relay hop, a misconfigured outbound provider, or
failing DKIM signing passes today's check indefinitely.

Everything needed for the receiving half already exists:

- The passive **email check** (`server/internal/checkers/checkemail/`) owns a
  unique secret address `<48-hex-token>@<inbox-domain>`; inbound mail to it is
  turned into an "up" result by the JMAP handler
  (`server/internal/handlers/emailcheck/handler.go`, registered at
  `server/internal/app/server.go:1270`).
- The passive overdue machinery (`executePassiveJob`,
  `server/internal/checkworker/worker.go:1307`) already flips a passive check
  down when no signal lands within its period — a ready-made delivery
  deadline.

What's missing is only the **sending half** and a little enrichment on the
receiving side.

## Proposal

Compose the two existing check types instead of building new infrastructure:
an opt-in **send mode** on the SMTP check submits a real, system-generated
probe email through the monitored server, addressed to a **paired email
check's existing tokenized address**. The email check confirms delivery
exactly the way it already confirms any inbound signal; new mail headers
carry the source-check identity and send time so the delivery result can be
attributed and timed.

The division of labor:

- **SMTP check** = submission health (synchronous: the monitored server
  accepted the message).
- **Paired email check** = end-to-end delivery health (passive: probes keep
  arriving within the period; silence → down via the existing overdue logic).

Submission failures and delivery failures are different problems with
different fixes; modeling them as two checks gives each its own incident
lifecycle for free.

### Sending side (worker)

New `SMTPConfig` fields (`server/internal/checkers/checksmtp/config.go`):

- `send_email` (bool, default false — existing checks are untouched),
- `mail_from` (string, required when `send_email` is set; the monitored
  server's sender policy usually dictates it),
- `delivery_check_uid` (string, required when `send_email` is set) — a
  reference to an **email check in the same org**. The server resolves it to
  the concrete `<token>@<inbox-domain>` recipient when building the check
  job; the worker never needs the token semantics, just the address.

Validation (`Validate` / check handlers): `send_email` requires both fields;
the referenced check must exist, be `CheckTypeEmail`, and belong to the same
org (the reference-not-address indirection is what prevents one org pointing
probes at another org's check address with SolidPing's blessing). If the
referenced check is deleted later, job dispatch fails loudly with an error
result rather than silently skipping the send. Reject `send_email: true`
when the instance has no configured `email_inbox`.

After the existing EHLO/STARTTLS/AUTH sequence the checker issues
`MAIL FROM` / `RCPT TO` / `DATA` / `QUIT` on the same `textproto.Conn` it
already drives. The message is **entirely system-generated** — no
user-supplied subject or body, ever (anything else turns the feature into a
spam template engine) — and carries:

```
X-SolidPing-Check: <sending SMTP check uid>
X-SolidPing-Sent-At: <RFC3339 send time>
```

No HMAC signature is needed: the tokenized recipient address is already the
unguessable per-check secret, which is precisely what the earlier
fixed-probe-address design would have had to re-add via signed headers.
Header stripping by an intermediate MTA degrades gracefully — the email
check still ticks up, only attribution/latency is lost.

The SMTP check's own result reflects submission only: `250` after DATA → up
with `submission_ms` in the details; any rejection → down with the server's
reply.

### Receiving side (server)

Extend the existing emailcheck JMAP handler — no new handler. When the
resolved check receives a message, parse the two optional headers (the
`findHeader` helper at `handler.go:162` already exists) and, when present,
enrich the recorded result output with:

- `sourceCheckUid` — the sending SMTP check,
- `sentAt` and `latencyMs` — JMAP `receivedAt` minus `Sent-At`. Clamp or
  drop nonsense values (future timestamps, negatives): the worker clock and
  the mail path's clocks are not the same. Record latency as a metric too so
  it can be charted.

Messages without the headers behave exactly as today — full backward
compatibility with human/heartbeat-style uses of the email check.

### Delivery deadline

None to build. The paired email check's period **is** the deadline
(`executePassiveJob`). Document the sizing rule: email check period ≥ SMTP
check interval + worst acceptable delivery time (greylisting can legitimately
add minutes). Enforce a minimum interval (e.g. 60s) on send-mode SMTP checks
so the inbox isn't flooded.

### UI (dash0)

- SMTP check form: send-mode toggle revealing `mail_from` and an email-check
  picker (same-org email checks only). Follow the design reference.
- Email check result details: render latency and a link to the source SMTP
  check when the enriched fields are present; the SMTP check page can
  likewise link to its paired delivery check (the pairing is in its config).
- Several SMTP checks *may* target the same email check (headers keep
  arrivals attributable), but 1:1 pairing is the recommended and documented
  setup — the passive up/down conflates senders otherwise.

### Guardrails

- Check whether anything gates on `labelSafe`
  (`server/internal/checkers/checkerdef/types.go:304`) — ad-hoc
  validate/diagnose execution of a send-mode config has a real side effect
  (an email); decide whether those flows skip the send stage.
- Docs: SMTP check page documents the mode, the pairing, interval sizing,
  and greylisting semantics.

### Out of scope (phase 2)

Delivering probes to **customer-owned mailboxes** — via an org-scoped DNS
TXT opt-in (`_solidping.<domain>` naming the org, so one customer's opt-in
can't be exploited by another org's checks) and/or per-address
confirmation-link verification. This spec deliberately restricts recipients
to the instance's own inbox, which makes a non-consenting recipient
impossible by construction.

Also explicitly rejected: a fixed instance-wide probe address with
HMAC-signed correlation headers and a dedicated deadline job. Reusing the
email check's tokenized address makes the signature, the new JMAP handler,
and the deadline machinery all unnecessary.

## Open questions

- Should the SMTP form offer inline creation of the paired email check
  ("create a delivery check for me") to reduce the two-check setup friction?
  Nice-to-have, not required for v1.
- Exact result-output key names — follow the handler's existing camelCase
  keys (`emailcheck/handler.go:37`).

## Resolved open questions

Answered by Florent (2026-08-19). These are directives — implement exactly this.

**Q: Should the SMTP form offer inline creation of the paired email check ("create a
delivery check for me") to reduce the two-check setup friction? Nice-to-have, not
required for v1.**

**Decision: No — do not build it in this spec.** The SMTP check form gets only the
email-check *picker* (same-org `CheckTypeEmail` checks). The user is expected to create
the email check first and then pair it. Do not add any check-creation path inside the SMTP
form, and do not add a "create one for me" affordance. If the friction proves real it
becomes its own follow-up spec once the core send/receive loop has shipped.

**Q: Exact result-output key names — follow the handler's existing camelCase keys
(`emailcheck/handler.go:37`).**

**Decision: Settled as written — follow the existing camelCase convention** at
`server/internal/handlers/emailcheck/handler.go:37`. Use `sourceCheckUid`, `sentAt` and
`latencyMs` as named in the Proposal, matching the surrounding keys rather than inventing a
new casing. No further decision needed here.
