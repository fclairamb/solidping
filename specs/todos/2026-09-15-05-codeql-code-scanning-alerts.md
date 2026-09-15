---
model: opus
effort: high
---

# 56 open CodeQL alerts on the repo, and none of them has been triaged

## Problem

GitHub code scanning (CodeQL, default setup, `remote` threat model, weekly +
on push) was enabled on 2026-09-13 and its first run left **56 open alerts**
at <https://github.com/fclairamb/solidping/security/code-scanning>: 14 on
`web/dash0` (JavaScript/TypeScript) and 42 on `server` (Go). Nothing has
been fixed or dismissed since, so the page is a wall of red that hides the
next real finding. `golangci-lint` runs `gosec`, but CodeQL is a different
engine: a `//nolint` comment does nothing for it, and the only two ways an
alert leaves the list are a code change that removes the flow, or an
explicit dismissal on GitHub with a reason.

Reading every alert against the code (all 56, flows pulled from the SARIF of
analysis `1775846973`), they fall into three buckets:

- **real defects** worth a code change (about a third),
- **noise** the scanner cannot see through (bounded allocations, escaped
  output, protocol-mandated hash functions), and
- **deliberate design choices** that need a written rationale, not a patch.

The full inventory is below. The goal of this spec is to get the open count
to **zero** with every non-fix carrying a one-line justification a future
reader can check.

### Inventory

| Rule | Alerts | Sites | Verdict |
|---|---|---|---|
| `js/clear-text-storage-of-sensitive-data` | 1, 2, 3, 5 | `web/dash0/src/api/client.ts:93-99`, `src/lib/oauth-handoff.ts:59` | won't fix (design) |
| `js/clear-text-storage-of-sensitive-data` | 4 | `src/contexts/AuthContext.tsx:236` (the org **slug**) | false positive |
| `js/incomplete-url-substring-sanitization` | 6 | `e2e/account-security-2fa.spec.ts:122` | fix (tighten the test) |
| `js/incomplete-sanitization` | 7 | `src/lib/status-page-badge-embed.ts:38` | **fix** |
| `js/insecure-randomness` | 8–14 | `e2e/login-2fa.spec.ts`, `e2e/org-members-admin-gate.spec.ts` | fix (shared helper) |
| `go/clear-text-logging` | 15–18 | Slack/Teams/Discord command handlers logging the check `url` | **fix** |
| `go/allocation-size-overflow` | 19–30 | twelve `make(map, len(a)+len(b))` | false positive |
| `go/weak-sensitive-data-hashing` | 31 | `checksip/checker.go:747` (MD5, SIP digest) | won't fix (protocol) |
| `go/weak-sensitive-data-hashing` | 32 | `ovhsms/client.go:354` (SHA-1, OVH signature) | won't fix (protocol) |
| `go/weak-sensitive-data-hashing` | 33 | `passwords/bcrypt.go:38` (SHA-256 pre-hash before bcrypt) | false positive |
| `go/request-forgery` | 34–39 | betterstack importer, freebox, msteams, slack `response_url`, twilio, jmap | won't fix (design) |
| `go/uncontrolled-allocation-size` | 40 | `testapi/handler.go:488` (`/fake?slowResponse=` bytes, unbounded) | **fix** |
| `go/uncontrolled-allocation-size` | 41 | `slo/window.go:72` (`?months=`, unbounded) | **fix** |
| `go/uncontrolled-allocation-size` | 42, 43 | `support/service.go:1047,1087` (limit clamped to 500/1000) | fix (drop the hint) |
| `go/cookie-secure-not-set` | 44, 45 | `auth/handler.go:109,529` (the `access_token` cookie) | **fix**, then won't fix |
| `go/cookie-secure-not-set` | 46 | `testapi/handler.go:296` (`/fake?setCookie=` fixture) | used in tests |
| `go/cookie-secure-not-set` | 47, 48 | `statuspagelock.go:178,192` (`Secure: isTLS(req)`) | won't fix (documented) |
| `go/insecure-hostkeycallback` | 49 | `checksftp/checker.go:105` (`InsecureIgnoreHostKey`) | fix (optional pin) |
| `go/insecure-hostkeycallback` | 50 | `checkssh/checker.go:215` (capture-only callback) | **fix** |
| `go/unvalidated-url-redirection` | 51 | `testapi/handler.go:286` (`/fake?redirectTo=`) | **fix** |
| `go/reflected-xss` | 52, 53 | `middleware/metrics.go:37`, `middleware/timeout.go:131` | false positive |
| `go/disabled-certificate-check` | 54 | `checkrdp/checker.go:240` | won't fix (design) |
| `go/cookie-httponly-not-set` | 55, 56 | `auth/handler.go:109,529` | **fix** |

## Proposal

One batch, two kinds of work: code changes for the true positives, and API
dismissals (with the reason recorded) for the rest. Do the code first, let
CodeQL re-run on the branch, then dismiss only what is still open, so a
dismissal never hides an alert the code change should have removed.

### A. Code changes

**A1. The session cookie gets `HttpOnly`, `SameSite=Lax` and `Secure`
(alerts 44, 45, 55, 56).** `setAccessTokenCookie` in
[auth/handler.go:109](server/internal/handlers/auth/handler.go#L109) and
`clearAuthCookie` at line 529 set the `access_token` cookie with nothing
but `Path` and `MaxAge`. The cookie is read only server-side
([middleware/auth.go:532](server/internal/middleware/auth.go#L532) and the
`realtimews` handshake); the SPA never touches `document.cookie` for it (the
only `document.cookie` write in `web/dash0/src` is the sidebar state). So
`HttpOnly: true` costs nothing and closes the one real XSS-to-session-theft
path on this cookie. Add `SameSite: http.SameSiteLaxMode` and `Secure:
isTLS(req)` with the same rationale as
[statuspagelock.go:182-186](server/internal/statuspagelock/statuspagelock.go#L182-L186)
(a `Secure` cookie on a plain-HTTP self-hosted instance is silently dropped
and the MCP consent flow would look broken). Move `isTLS` out of
`statuspagelock` into a shared helper so both cookie writers use the one
definition; both writers take the request from now on. CodeQL will keep
flagging the dynamic `Secure` on 44/45 exactly as it does on 47/48; those
four are then dismissed together (B5).

**A2. Chat-command logs redact the check URL (alerts 15–18).** The four
sites —
[slack/mention_commands.go:93](server/internal/integrations/slack/mention_commands.go#L93),
[slack/commands.go:163](server/internal/integrations/slack/commands.go#L163),
[msteams/mention_commands.go:97](server/internal/integrations/msteams/mention_commands.go#L97),
[discord/commands.go:142](server/internal/integrations/discord/commands.go#L142)
— log the `url` a user typed into `/solidping checks add <url>` verbatim, so
`https://user:secret@host/` lands in the log with the secret. Log
`(*url.URL).Redacted()` (stdlib, replaces the password with `xxxxx`) through
one helper (`checkhttp.RedactURL(string) string`, tolerant of unparsable
input) and use it at all four sites. While there, follow each flow to its
source (`checkhttp/config.go:565`, the `password` key read by
`NormalizeConfig`) and confirm the wrapped `err` on the same log line cannot
carry the credential either; `stringConfigValue` only emits "must be a
string", so it should not, but the check is part of the change.

**A3. Two unbounded user-driven allocations get a ceiling (alerts 40, 41).**
- `/fake?slowResponse=<iterations>,<bytes>,<delay>`: iterations are capped at
  100 and delay at `MaxFakeDelayMS`, but `bytes` only checks `>= 1`
  ([testapi/handler.go:399](server/internal/handlers/testapi/handler.go#L399)),
  then `make([]byte, params.bytes)` runs per iteration
  ([:488](server/internal/handlers/testapi/handler.go#L488)). `/fake` is
  deliberately public and rate-limited (`server.go:2178-2190`), so one
  request with `bytes=2000000000` is enough to OOM the instance. Cap bytes
  per chunk (64 KiB) and the product iterations×bytes (1 MiB), reject above
  with the existing `ErrSlowResponseBytes`, and document the ceiling on the
  `/fake` wiki page.
- `GET /orgs/{org}/slos/{uid}/history?months=N` rejects `<= 0`
  ([slos/handler.go:154](server/internal/handlers/slos/handler.go#L154)) but
  has no upper bound, and `PreviousMonthWindows` allocates `count` windows
  ([slo/window.go:72](server/internal/slo/window.go#L72)). The UI asks for
  12 (`useSloHistory`). Clamp at 36 in the handler with a 400 above it, and
  add the bound to the OpenAPI description of `months`.

**A4. Support-inbox capacity hints go (alerts 42, 43).** `ListThreads` and
`ListMessages` clamp `limit` to 500/1000 before `make([]*T, 0, limit)`
([support/service.go:1047](server/internal/support/service.go#L1047),
[:1087](server/internal/support/service.go#L1087)); the alerts are false
positives, but the hint buys nothing on a paged query. Drop the capacity
(`var threads []*models.SupportThread`) so the flow disappears for good
rather than being dismissed.

**A5. Markdown alt text escapes the backslash (alert 7).**
`escapeMarkdownAltText` in
[status-page-badge-embed.ts:38](web/dash0/src/lib/status-page-badge-embed.ts#L38)
escapes `[` and `]` but not `\`, so a page name ending in `\` produces `\\]`
— an escaped backslash followed by a live `]` that closes the alt text early.
Escape the backslash first: `value.replace(/[\\[\]]/g, "\\$&")`, plus a unit
case for `foo\]bar` in the existing test file.

**A6. The SSH fingerprint check verifies inside the callback (alert 50).**
`verifyFingerprint` in
[checkssh/checker.go:213-219](server/internal/checkers/checkssh/checker.go#L213-L219)
installs a `HostKeyCallback` that records the fingerprint and returns `nil`,
then compares after the dial (line 244). Compare inside the callback and
return a typed mismatch error (keeping the captured value for the result
output), so the handshake itself is what refuses an unexpected key. Behaviour
for the operator is unchanged: same `StatusDown`, same message quoting both
fingerprints; only the point of rejection moves. CodeQL stops flagging a
callback that can return an error.

**A7. The SFTP checker learns an optional host-key pin (alert 49).**
`SFTPConfig` ([checksftp/config.go:19](server/internal/checkers/checksftp/config.go#L19))
has no way to say which host key is expected, so the client is built with
`ssh.InsecureIgnoreHostKey()`. Add `host_key_fingerprint` (same `SHA256:…`
format `checkssh.Fingerprint` produces) to the config, its `FromMap`, the
JSON schema / OpenAPI, the dash0 SFTP form and the check-type wiki page. When
set, the callback verifies and a mismatch is `StatusDown` with both values
in the output; when unset, keep the ignore callback but always write the
observed fingerprint to `output.host_key_fingerprint` so an operator can copy
it into the pin. The unset branch is still `InsecureIgnoreHostKey`, so alert
49 is dismissed afterwards with the pin as the rationale (B6).

**A8. `/fake?redirectTo=` only redirects to itself (alert 51).**
`validateRedirectURL`
([testapi/handler.go:455-471](server/internal/handlers/testapi/handler.go#L455-L471))
blocks a hand-written list of private prefixes (and misses `172.17-31.`,
`169.254.`, `0.0.0.0`, `[::1]`), which is the wrong question anyway: this is
a client-side redirect on a public endpoint of the production domain, so the
risk is an open redirect (`solidping.io/api/v1/fake?redirectTo=https://evil`),
not SSRF. Accept only a relative path (`/…`, not `//…`) or an absolute URL
whose scheme+host equal the request's own origin; anything else is the
existing 400. No test or dashboard caller passes `redirectTo` today, and
redirect-following checks can still be exercised against the same host.

**A9. E2E fixtures stop using `Math.random()` (alerts 8–14) and the QR test
asserts on the hostname (alert 6).** A dozen sites under `e2e/` (`fixtures.ts` and ten spec files) build
unique e-mails/slugs from `Date.now() + Math.floor(Math.random() * 1000)`.
Add one `uniqueStamp()` helper to `e2e/fixtures.ts` backed by
`crypto.randomUUID()` and use it everywhere (it also removes a real
same-millisecond collision risk under parallel workers). In
[account-security-2fa.spec.ts:122](web/dash0/e2e/account-security-2fa.spec.ts#L122)
replace `req.url().includes("qrserver.com")` with "any request whose
`new URL(req.url()).origin` is not the app's own origin" — stricter than the
substring, and it states what the test means (no request leaves the page).

### B. Dismissals

Everything below is dismissed through the API, one call per alert, so the
reason and the comment are on the alert itself:

```bash
gh api -X PATCH repos/fclairamb/solidping/code-scanning/alerts/<n> \
  -f state=dismissed -f dismissed_reason=<false positive|won't fix|used in tests> \
  -f dismissed_comment='<one line, see below> (spec 2026-09-15-05)'
```

- **B1. Tokens in `localStorage` (1, 2, 3, 5) — won't fix.** The SPA keeps
  the access/refresh tokens in `localStorage` by design (`client.ts`
  `setSession`, the OAuth handoff). Moving the browser session to the
  `HttpOnly` cookie the server already sets is a real option — the
  middleware already falls back to it — but it is its own spec (token
  refresh, the WebSocket handshake, CSRF), not a side effect of this one.
  Alert **4** is a plain **false positive**: `solidping_org` is the org slug.
- **B2. `len(a)+len(b)` map capacities (19–30) — false positive.** Sums of
  two in-memory map lengths cannot overflow an `int`.
- **B3. Hash functions (31, 32, 33).** 31: MD5 is the digest algorithm of
  SIP authentication (RFC 3261 §22), the checker is speaking the protocol —
  **won't fix**. 32: `$1$` + SHA-1 is OVH's API signature scheme — **won't
  fix**. 33: SHA-256 is a length-normalising pre-hash feeding bcrypt (the
  72-byte limit), not the password hash — **false positive**.
- **B4. Requests to operator-provided URLs (34–39) — won't fix.** Each one
  is the feature: 34 BetterStack pagination (`next` is already pinned to the
  base scheme+host at `betterstack.go:280`), 35 the user's own Freebox
  (defaults to `mafreebox.freebox.fr`, a LAN device), 36 the `serviceUrl` of
  a signature-verified Bot Framework activity, 37 Slack's signed
  `response_url`, 38 Twilio (base URL derived from the region, not
  user-settable), 39 the JMAP session URL an instance admin submits to
  `/system/email-inbox/test`. Whether the hosted instance should refuse
  private address ranges for integrations is a separate question; it is not
  what these alerts measure.
- **B5. Dynamic `Secure` (44, 45 after A1; 47, 48) — won't fix.**
  `Secure: isTLS(req)`, reason in the `SetCookie` doc comment.
- **B6. `InsecureIgnoreHostKey` when no pin is set (49 after A7) — won't
  fix.** Reachability checks against a host the operator owns; the pin
  exists for anyone who wants verification.
- **B7. The `/fake` cookie fixture (46) — used in tests.** `/fake?setCookie=`
  exists to feed the HTTP checker's cookie jar.
- **B8. Middleware `Write` wrappers (52, 53) — false positive.** The two
  sinks are the metrics/timeout `ResponseWriter` wrappers; the four sources
  CodeQL traces all escape before writing: badges `escapeXML`
  (`badges/svg.go:96`), the status-page badge (same renderer), status-page
  `<meta>` tags (`html.EscapeString`, `status0_meta.go:117-134`), TwiML
  (`twimlEscaper`, `twiliocb/handler.go:398-410`).
- **B9. RDP `InsecureSkipVerify` (54) — won't fix.** Leaf-only inspection of
  routinely self-signed RDP certificates, per the comment at
  `checkrdp/checker.go:236-238`.

### Acceptance

- After the batch lands on `main` and CodeQL has re-run,
  `gh api 'repos/fclairamb/solidping/code-scanning/alerts?state=open'`
  returns `[]`.
- Every dismissed alert carries a `dismissed_comment` that names the reason
  and this spec.
- Tests: `RedactURL` (userinfo stripped, garbage passthrough), the
  `slowResponse` and `months` ceilings (400 above, OK at the bound), the
  Markdown backslash case, SSH mismatch refused at handshake, SFTP pin
  match/mismatch/unset (fingerprint in output), `/fake` redirect accepts
  `/path` and same-origin, refuses `//evil`, `https://evil`, `[::1]`.
- `make lint` and the Go/Playwright suites stay green; no new
  `//nolint:gosec`.

### Open questions

- Should the hosted instance move the SPA session to the `HttpOnly` cookie
  (B1)? Worth its own spec; noted here so the four dismissals are not read
  as "this is fine forever".
- `months` ceiling: 36 is a guess above the UI's 12. Anything the SLO
  history page will ever ask for is fine; the point is that a bound exists.
