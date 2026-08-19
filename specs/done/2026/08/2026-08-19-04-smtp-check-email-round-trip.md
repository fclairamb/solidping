---
model: sonnet
effort: high
---

# SMTP check can send a real probe email, delivered to a paired passive email check, to verify the full transmission loop

> **⚠️ SUPERSEDED IN PART — read `## Revised design (2026-08-19)` at the bottom FIRST.**
> Florent changed how the recipient is stored. Where this document and the revision
> disagree, **the revision wins**. Everything the revision does not mention still stands.

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

## Implementation Plan

### Config shape (`checksmtp/config.go`)
Add `SendEmail bool` (`send_email`), `MailFrom string` (`mail_from`),
`DeliveryCheckUID string` (`delivery_check_uid`) to `SMTPConfig`, with `FromMap`/`GetConfig`
support. `Validate()` gets shape-only checks: `send_email` requires non-empty `mail_from`
and `delivery_check_uid`. Cross-check existence/org/type/email_inbox validation needs DB
access the checker layer doesn't have, so it lives in `checks/service.go` instead (below) —
mirroring exactly how `tunnelCheckUid` splits shape validation (checker) from reference
validation (service, DB-backed).

### Guardrail design: resolving the reference without letting it leak (the core decision)
`Checker.Execute` has exactly two callers in this codebase (verified, not assumed):
`checkworker/worker.go:1167` (real job dispatch — server-resolved, org-scoped, safe) and
`checkjs/checker.go:307` (a JS check's `check()` sub-check helper, which builds a raw config
map from the JS SCRIPT ITSELF and calls `checker.Execute` directly, bypassing job dispatch
entirely). `validate_check` (MCP) only calls `checker.Validate` (config shape, no I/O) and
`diagnose_check` (MCP) only reads existing DB rows — neither ever calls `Execute`, so neither
needs gating.

The JS sub-check path is the real side-effect risk `labelSafe` doesn't cover: a JS check
(itself `labelUnsafe`/`requires:scripting`) could otherwise construct
`{send_email:true, mail_from:..., delivery_check_uid:"..."}` from script and fire a real
email with no org-scoping check ever run (job dispatch's DB-backed resolution is skipped
entirely by this path).

Fix: the concrete recipient address is **never** a JSON-decodable config field. It travels
context-only, mirroring the existing `WithTunnelDialer`/`TunnelDialerFrom` and
`WithIPVersion`/`IPVersionFrom` seams in `checkerdef`:
- `checkerdef.WithSMTPDeliveryRecipient(ctx, addr)` / `checkerdef.SMTPDeliveryRecipientFrom(ctx)`.
- The resolved address is computed server-side (see below) and threaded onto the config map
  under an internal key (`smtpResolvedRecipient`) that `SMTPConfig.FromMap` does NOT parse
  (exactly like `tunnelCheckUid`/`timeout` are read generically off the raw map by
  `worker.go`, never through a checker's own `FromMap`). `worker.go`'s per-job-type step
  reads that raw key and calls `WithSMTPDeliveryRecipient` on `execCtx`, right next to the
  existing tunnel/ipVersion context wiring, gated on `checkJob.Type == smtp`.
- `checksmtp.Execute` reads the address ONLY via `SMTPDeliveryRecipientFrom(ctx)`. When
  `SendEmail` is true and the context carries no address (JS sub-check path, or any other
  direct `Execute` call outside job dispatch), it returns an explicit `StatusError` result
  ("no delivery recipient resolved") — loud, never a silent skip. This single mechanism
  covers both required guardrails at once: it blocks the JS sub-check bypass structurally
  (its `execCtx` never gets the context value, since `checkJob.Type` for a JS check is
  never `smtp`), and it produces the same loud-error behavior needed for "the referenced
  check was deleted before dispatch" (below).
- Documented explicitly in checksmtp's doc comment and in the SMTP docs page.

### Resolving the reference (server-side, before dispatch)
`delivery_check_uid` → concrete `<token>@<inbox-domain>` resolution needs DB access, which
deported agents never have — so it must happen before the job ever leaves the server
process, not inside the shared `worker.go` execution path (that code also runs verbatim on
an agent). It is added to `checkworker/backend.DirectBackend`, in a new step
`resolveSMTPDelivery` called right after `mergeClaimedSecrets` in both `ClaimJobs` and
`ClaimJobsForCheck` (mirrors that function's shape exactly, including its
error-result-on-failure behavior):
- Skip anything whose `Type != "smtp"` or whose config lacks `send_email: true`.
- Look up `delivery_check_uid` via `dbService.GetCheck(ctx, job.OrganizationUID, uid)` — this
  call is ALREADY org-scoped (a cross-org uid reads as not-found), which is what makes the
  reference-not-address indirection actually enforceable at the resolution boundary too, not
  only at write time.
- Require `Type == "email"`; on any failure (not found, wrong type, wrong org) OR when the
  instance has no `email_inbox` configured, submit an explicit `StatusError` result (mirrors
  `submitSecretsError`) and release the lease — satisfies "deleted referenced check fails
  loudly, never silently skips."
- On success, set `job.Config["smtpResolvedRecipient"] = "<token>@<addressDomain>"`
  (`addressDomain` from the `email.inbox` system parameter, `token` from the target check's
  `config.token`) — always recomputed, never trusted from storage, so nothing a user could
  have PATCHed into their own check's public config survives to dispatch.

**Known scope limit (documented, not silently dropped):** this resolution step is added to
`DirectBackend` only (the in-process path `make dev` and the test suite exercise). The
deported-agent claim path (`WSBackend`/`ClaimJobsForAgent`) is NOT wired for delivery
resolution in this pass — a send-mode SMTP check scheduled onto a region served only by a
deported agent will get no resolved recipient and will surface the same loud
"no delivery recipient resolved" error rather than silently sending nowhere. Flagged in the
SMTP docs page as a current limitation.

### Sending (`checksmtp/checker.go`)
After the existing EHLO/STARTTLS/AUTH sequence and before `QUIT`: if `cfg.SendEmail`, resolve
the recipient via context; if absent, return the explicit error result described above.
Otherwise issue `MAIL FROM:<mail_from>` / `RCPT TO:<recipient>` / `DATA` with a
system-generated body (`X-SolidPing-Check: <check-uid-from-ctx-or-config>`,
`X-SolidPing-Sent-At: <RFC3339>`, empty subject/body — no user input in the message ever)
then `.` / `QUIT`. `250` after DATA → up, `submission_ms` in metrics; any rejection → down
with the server's reply, mirroring the existing rejection-result shape used elsewhere in this
checker.

### Write-time validation (`checks/service.go`, new file `smtp_delivery.go`)
Mirrors `tunnel.go`'s `validateTunnelConfig` shape exactly:
- `validateSMTPDeliveryConfig(ctx, orgUID, checkType, effectiveConfig)`: shape (mail_from +
  delivery_check_uid present when send_email), cross-check existence/org/`CheckTypeEmail`
  (via `s.db.GetCheck`), and `email_inbox` configured (via
  `s.db.GetSystemParameter(ctx, jmap.SystemParameterKey)`). Called from `CreateCheck` and
  from `applyConfigUpdate` (PATCH) right next to `validateTunnelConfig`, since
  `UpdateCheck` never calls `checker.Validate` either.
- A separate pure `validateSMTPSendInterval(checkType, config, period)`: send-mode SMTP
  requires period ≥ 60s. Called at the end of `CreateCheck` and `UpdateCheck` once both the
  effective config and effective period are known (mirrors how `RegionSpread`'s
  `effectivePeriod` is computed from `check.Period`/`update.Period`).

### Receiving (`emailcheck/handler.go`)
Parse `X-SolidPing-Check` / `X-SolidPing-Sent-At` via the existing `findHeader`. When both
present and `Sent-At` parses as RFC3339: compute `latencyMs = receivedAt - sentAt`; clamp/drop
(never record) when negative or `sentAt` is in the future relative to `receivedAt` — the
worker clock and the mail path's clock are not the same box. On success, add
`sourceCheckUid`/`sentAt`/`latencyMs` to `output` and record `latencyMs` as a metric too.
Missing/malformed headers → byte-identical output to today (no new keys at all).

### Dash0 UI
- `components/checks/form/types/mail.tsx` (`smtpModule`): add `sendEmail`/`mailFrom`/
  `deliveryCheckUid` to `SmtpState`; a `Switch` reveals `mail_from` input + a same-org email
  check picker. The picker fetches `useChecks(org, {type: "email", limit: 100})` directly
  (via the existing `useCheckFormFields()` context's `org` — no new context plumbing needed,
  unlike `tunnelCheckUid` which is cross-type and lives in `check-form.tsx` itself). Per the
  resolved open question: NO "create a delivery check for me" link/button anywhere in this
  UI — empty-state is plain text only, no navigation affordance into check creation.
  `toConfig` adds required-field errors when `sendEmail` is set without `mailFrom`/
  `deliveryCheckUid`.
- New `components/checks/email-delivery-card.tsx` (mirrors `dnsbl-card.tsx`): renders on
  `checks.$checkUid.results.$resultUid.tsx` when `sourceCheckUid`/`sentAt`/`latencyMs` are
  present in `output`, with a link to the source SMTP check; those keys are stripped from the
  raw JSON dump exactly like `DNSBL_OUTPUT_KEYS`.
- New `components/checks/smtp-delivery-detail.tsx` (mirrors `tunnel-detail.tsx`'s
  `TunnelVia`/`TunnelDependents`): `DeliveryVia` on the SMTP check page links to its paired
  email check; `DeliverySources` on the email check page lists SMTP checks pointing at it.
  Wired into `checks.$checkUid.index.tsx` next to the existing `TunnelVia`/`TunnelDependents`.

### Docs (`web/docs`)
Extend the SMTP check page: send mode, the pairing model, interval-sizing rule (email check
period ≥ SMTP interval + worst acceptable delivery time; greylisting can add minutes), the
60s minimum, and the deported-agent limitation above.

### Tests
Backend: fake SMTP server assertions (envelope + headers actually sent) for send-mode;
negative control (send_email false emits no mail commands); cross-org/wrong-type/missing
`delivery_check_uid` rejected at write time; deleted-reference-at-dispatch loud error;
JS-subcheck-cannot-send guardrail test; min-interval-60s enforcement; receiving-side
enrichment present/absent/clamped-nonsense. Frontend: form toggle + picker + validation;
result-detail card presence/absence.

## Revised design (2026-08-19)

Decided by Florent after the first implementation round, in response to the audit finding
that send mode did not work on private locations. **This section supersedes the parts of
the Proposal it contradicts.** Everything not mentioned here (system-generated message
only, the two attribution headers, submission-only SMTP result, receiving-side enrichment,
clamping, the 60s floor, the passive email check as the delivery deadline) is unchanged.

### What changes: store the recipient address, not a reference

The original design stored `delivery_check_uid` and resolved it to a concrete
`<token>@<inbox-domain>` recipient **at claim time**, server-side. That resolution was only
ever wired into the `DirectBackend` claim path, so send mode did not work on private
locations — and wiring it into `ClaimJobsForAgent` raised an unresolved question about
handing a resolved recipient to a **system** agent shared across orgs.

**Store the email target directly on the SMTP check instead.** It becomes ordinary check
config that flows to every worker and agent through the normal path:

- **`delivery_to`** (string) — the recipient address, **the operative field**. Required
  when `send_email` is set.
- **`delivery_check_uid`** (string, **optional — bonus**) — retained only for attribution
  and the UI pairing links (the "delivery via" / "delivery sources" cards). It is no longer
  a security control and must not be treated as one. If supplied it must still name an
  existing `CheckTypeEmail` check **in the same org** — bonus metadata must not become its
  own hole — but it is not required, and nothing resolves it at claim time any more.

Delete the claim-time resolution path entirely: no `resolveSMTPDelivery` in the backend
claim flow, no context threading of a resolved recipient, no `smtpResolvedRecipient` config
injection. The checker reads `delivery_to` straight from its config. This is what makes the
agent / private-location path work with no sealing question to answer.

### Validation of `delivery_to` — both rules are required

1. **It must be a single bare RFC 5322 address**, validated exactly the way `mail_from`
   now is: `net/mail.ParseAddress` must succeed **and** the parsed `addr.Address` must
   equal the input string. Reuse `checksmtp.ValidateMailFrom`'s approach (extract a shared
   helper rather than copying it). This is what keeps CRLF, NUL, display-name and
   quoted-local-part forms out of the SMTP command line and the header block — the
   injection hole closed in `c21b5b7bd` must not reopen through this new field.
2. **Its domain must equal the instance's configured inbox domain** (from `email_inbox`).
   Reject `send_email: true` when the instance has no `email_inbox` configured, as before.

Enforce both on create **and** on PATCH (against the merged/effective config), mirroring
what `mail_from` and the 60s interval floor already do.

### Accepted residual risk — recorded deliberately

The Proposal justified the reference-not-address indirection as *"what prevents one org
pointing probes at another org's check address with SolidPing's blessing."* **That
mechanism is intentionally given up here.** With free-text `delivery_to` constrained only
by domain, an org that learns another org's tokenized address can aim probes at it, which
would tick that other org's email check up.

Florent was shown this trade-off explicitly and accepted it (2026-08-19). The domain
restriction is what still holds: a recipient outside the instance's own inbox domain
remains impossible, so the Out-of-scope guarantee — *"restricts recipients to the
instance's own inbox, which makes a non-consenting recipient impossible by construction"* —
is preserved. Do not silently reintroduce a same-org check on `delivery_to`; if you believe
it should be reinstated, say so in your report rather than implementing it.

### Consequence to handle: the `checkjs` sub-check path

The previous implementation threaded the resolved recipient through the request **context**,
which structurally prevented a `js` check from triggering a real send: `applySMTPDeliveryContext`
only fires for a job whose type is `smtp`, so a `browser`/`smtp` sub-check invoked from the JS
sandbox's `solidping.check(...)` (`server/internal/checkers/checkjs/checker.go:307`, a genuine
second caller of `Checker.Execute`) always missed and returned a loud `StatusError`.

**Making `delivery_to` a plain config field removes that protection.** A `js` check could
construct an SMTP sub-check config with `send_email: true` and a valid inbox-domain address
and cause real mail to be submitted. Bounded (our own inbox domain only), but no longer
impossible.

Handle it explicitly: keep send mode from executing when the checker is invoked as a JS
sub-check rather than as a dispatched `smtp` check job, and test it. Document what you chose
and why, with the verified caller set.
