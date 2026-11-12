---
model: sonnet
effort: high
---

# The email check says "delivered" without looking at who sent the mail or whether a real mailbox would accept it

GitHub Issue: https://github.com/fclairamb/solidping/issues/853

## Source

Filed from a production incident: an `smtp` → `email` round-trip pair was green while the same
relay, sending from another egress IP, was getting `550 5.7.1 … blocked using Spamhaus` from
Microsoft 365. The `email` check turns green as soon as a mail carrying the token lands; it never
evaluates SPF, DKIM, DMARC or whether the sending IP is on a DNS blocklist, so "delivered" does not
mean "a real recipient would accept it".

The reporter asks for optional, off-by-default validation "like a receiving MTA would", with a
configurable policy (`require_spf`, `require_dkim`, `require_dmarc`, `blocklists`) and every verdict
exposed in the result output even when the policy does not fail the check.

Where the report needs correcting: SolidPing is **not** the receiving MTA. It reads the inbox over
JMAP (`server/internal/jmap/`), so it never sees the SMTP connection, the connecting peer IP or the
envelope `MAIL FROM`. All of those have to be recovered from the headers the receiving server
added (`Received:`, `Return-Path:`, `Authentication-Results:`), which shapes the proposal below.

The issue's "Related" paragraph (one `smtp` + `email` pair per private location / region, so the
egress IP is meaningful) is a separate feature and is out of scope here.

## Problem

- `server/internal/checkers/checkemail/config/config.go:17-19`: `EmailConfig` holds only `Token`.
  `FromMap` / `GetConfig` (`:22-41`) and `ValidateSpec` (`config/validate.go:11-33`) know no other
  field, and `server/internal/checkers/schemas/email.json` describes only `token`.
- `server/internal/handlers/emailcheck/handler.go:152-174` (`HandleEmail`) resolves the check by
  token and calls `recordResult` (`:389-466`) without ever parsing `check.Config`. The status comes
  only from `extractTokenAndStatus` (`:186-236`: plus-address, `X-SolidPing-Status`, subject tag,
  default `up`). The output (`:419-425`) carries `message`, `from`, `subject`, `messageId`,
  `receivedAt`, plus the SMTP attribution of `enrichFromSMTPHeaders` (`:259-287`). Nothing about the
  sender's authenticity.
- The raw material is already fetched: `defaultEmailProperties` (`server/internal/jmap/methods.go:31-36`)
  requests `headers`, so `jmap.Email.Headers` (`types.go:176-179`, `:221`) holds every raw header,
  including `Received:` and `Authentication-Results:`. The full RFC 5322 message, which DKIM needs
  (the body hash is over the exact bytes), is **not** fetched: only `blobId` (`types.go:207`).
  `jmap.Client` keeps the session's `downloadURL` (`client.go:74`, `:173`) but has no blob
  download method.
- No SPF / DKIM / DMARC library is in `server/go.mod`.
- The blocklist logic exists in `server/internal/checkers/checkdnsbl/`: `DefaultBlocklists`
  (`config/config.go:33`), `ResolveBlocklists` (`:119`), `reverseIP` (`checker.go:289`),
  `isDNSBLErrorCode` / `partitionDNSBLAddrs` (`:311-342`, so a `127.255.255.x` "query refused"
  answer is not read as "listed") and `createResolver` (`:358`). Those helpers are unexported,
  inside the checker package.
- `HandleEmail` runs inside the JMAP manager's sync loop (`jmap/manager.go:543` `dispatch`), so any
  DNS lookup added there delays every other email of the batch unless it is bounded.

## Why a spec

- New check config fields and a schema / OpenAPI-visible change (`email.json`, the check form).
- Several subsystems: the JMAP client (blob download), the email-check handler, the DNSBL helpers
  (extraction to a shared package) and the frontend form.
- A new dependency and well over 150 lines with tests.
- Design decisions the reporter did not settle: which `Received:` hop to trust, trusting the
  provider's `Authentication-Results` versus verifying locally.

## Proposal

### Config (`checkemail/config`)

All optional; absent means today's behaviour exactly (no extra lookup, no extra output).

```yaml
config:
  token: …
  require_spf: pass          # "" (off) | none | softfail | pass
  require_dkim: true         # bool: at least one valid signature aligned with the From domain
  require_dmarc: pass        # "" (off) | pass
  check_blocklists: true     # look the sending IP up on DNS blocklists
  blocklists: [zen.spamhaus.org, bl.spamcop.net]   # empty = checkdnsbl DefaultBlocklists
  auth_verdict_status: down  # status when a requirement fails: down (default) | degraded
```

- `require_spf` is a minimum: `pass` accepts only `pass`; `softfail` accepts `pass`, `neutral`,
  `softfail`; `none` accepts anything but `fail` / `permerror`.
- `blocklists` set without `check_blocklists` implies `check_blocklists: true`.
- Validate the enums, the zone names (reuse the checkdnsbl zone validation) and a cap on the zone
  count (reuse `checkdnsbl/config/limits.go`) in `ValidateSpec`, so `sp checks validate` catches
  them offline. Update `FromMap` / `GetConfig` and regenerate `schemas/email.json`.
- If the repo's `degraded` status is not available to passive results, drop
  `auth_verdict_status` and always use `down` (check `internal/degraded` first).

### Evaluation (`internal/handlers/emailcheck/auth.go`, new)

`evaluateSenderAuth(ctx, email, cfg) senderAuth` runs only when at least one option is set, and
only when the token-derived status is `up` (an explicit `down` / `error` from the sender wins; do
not mask it).

1. **Sending IP.** Take the `Received:` header added by the receiving server: the topmost one whose
   `by` host matches the configured inbox's provider is the hop to trust; its `from … [IP]` is the
   sending peer. Rule (to implement and document): walk `Received:` top-down, skip hops whose
   `from` IP is private / loopback (the provider's internal relays), take the first public IP.
   Never trust hops below it for the verdict (a sender can forge them); they are reported as
   `received_ips` for information only.
2. **SPF / DKIM / DMARC.** Recommended (see Open questions): verify locally with
   `github.com/emersion/go-msgauth` (`dkim.Verify`, `dmarc.Lookup`) and an SPF library
   (`blitiri.com.ar/go/spf`), so the verdict does not depend on the inbox provider. That needs the
   raw message: add `Client.DownloadBlob(ctx, accountID, blobID)` to `jmap/client.go` (RFC 8620
   §6.2, `downloadURL` template), size-capped like the existing body cap (`body_cap_test.go`), and
   fetch it only for checks that enabled DKIM or DMARC. SPF uses the IP from step 1 and the
   `Return-Path:` domain (fallback: the `HELO` in that `Received:` hop, then the `From` domain).
   DMARC: the published policy of the `From` organisational domain, aligned with SPF or DKIM.
   As a cross-check, parse the provider's `Authentication-Results:` (first one, `authserv-id` of the
   provider) into `provider_auth` in the output; it never decides the status.
3. **Blocklists.** Move `reverseIP`, `isDNSBLErrorCode`, `partitionDNSBLAddrs` and the lookup loop
   out of `checkdnsbl/checker.go` into a small shared package (for example
   `internal/checkers/dnsblquery`) used by both the `dnsbl` checker and the email handler, without
   changing the `dnsbl` checker's behaviour (its tests must pass unchanged). Query the sending IP
   only (IPv4 and IPv6 as the `dnsbl` checker does).
4. **Bounds.** One overall deadline for the whole evaluation (5 s), lookups in parallel. A timeout
   or DNS error makes the verdict `temperror`, which never fails a `require_*` (report it, do not
   page on resolver noise); log it at WARN.

### Result

`recordResult` adds, when the evaluation ran:

```json
"auth": {
  "sending_ip": "203.0.113.7",
  "spf": "pass", "spf_domain": "example.com",
  "dkim": "pass", "dkim_domains": ["example.com"], "dkim_aligned": true,
  "dmarc": "pass", "dmarc_policy": "reject",
  "blocklisted_on": ["zen.spamhaus.org"],
  "provider_auth": {"spf": "pass", "dkim": "pass", "dmarc": "pass"},
  "received_ips": ["203.0.113.7", "10.0.0.4"]
}
```

When a requirement fails, the status becomes `auth_verdict_status` and the message says which one
(`"delivered, but sending IP 203.0.113.7 is listed on zen.spamhaus.org"`,
`"delivered, but SPF softfail for example.com"`). Add a metric `blocklisted_count`.

### Frontend (`web/dash0`)

In the email check form, an "Authentication checks" collapsible section with the fields above,
each with a `data-testid` (`email-require-spf-select`, `email-require-dkim-switch`,
`email-require-dmarc-select`, `email-check-blocklists-switch`, `email-blocklists-input`). On the
result / delivery card (`components/checks/email-delivery-card.tsx`), show the `auth` verdicts as
badges when present. i18n keys in every locale the app ships.

### Tests

- `checkemail/config`: `FromMap` / `GetConfig` round-trip, `ValidateSpec` rejects bad enums and
  zone names, no new key appears when nothing is set (backward compatibility).
- `emailcheck/auth_test.go`: `Received:` parsing (provider internal hops skipped, forged lower hops
  ignored, IPv6 literal), SPF / DKIM / DMARC with a stub resolver and a fixture message signed with
  a test key (go-msgauth's test helpers), blocklist listed / clean / `127.255.255.254` refused,
  overall timeout gives `temperror` and keeps the check `up`.
- `emailcheck/handler_test.go`: a check without options records exactly today's output; a check
  with `check_blocklists` and a listed IP records `down` with `blocklisted_on`; an explicit
  `+down` email is not overridden.
- `checkdnsbl`: existing tests pass unchanged after the extraction.
- `jmap`: `DownloadBlob` URL templating and size cap.
- One Playwright test that sets the options in the form and sees them persisted.

### Docs

`wiki/features/email-inbox-checks.md`: a "Sender authentication" section (what is verified, which
`Received:` hop is trusted and why, `temperror` semantics, cost: one blob download and a few DNS
lookups per email). Also mention the `smtp` → `email` pair as the way to monitor a relay's
deliverability.

## Open questions

1. **Verify locally, or trust the inbox provider's `Authentication-Results:`?**
   Recommended: verify locally (above), report the provider's header as `provider_auth`.
   Trade-off: local verification costs a blob download per email and a new dependency, but the
   verdict is ours and does not change when the inbox provider changes; trusting the header is a
   few lines and no download, but some JMAP servers (self-hosted Stalwart without milters) add no
   such header or a partial one.
2. **Which status when a requirement fails?** Recommended: `down` by default, `degraded` opt-in
   through `auth_verdict_status`. Trade-off: `down` pages people for a deliverability problem that
   is real but not an outage; `degraded` alone would be missed by those who route only `down`.

## Closing the issue

This spec closes fclairamb/solidping#853. The implementing PR body **must** carry a
`Closes #853` line so the squash-merge closes it. When the spec is archived to `specs/done/`,
verify the issue is closed and, if the merge did not close it, close it by hand:

    gh issue close 853 --repo fclairamb/solidping --comment "Implemented by <PR or commit>; spec: <archived spec path>"
