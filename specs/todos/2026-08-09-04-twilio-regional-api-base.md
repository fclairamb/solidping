---
model: sonnet
effort: high
---

# A Twilio account in the Ireland (ie1) region cannot be used — the API base is hardcoded to US1

## Problem

SolidPing always talks to `https://api.twilio.com`, Twilio's **US1** region.
Twilio's [regional editions](https://www.twilio.com/docs/global-infrastructure)
give each region its own API host — Ireland is `https://api.ie1.twilio.com` —
and an account provisioned in `ie1` is only reachable there. An operator who
chose the Ireland region for **EU data residency** therefore cannot use the
SMS/voice paging feature at all: every request lands on US1 and fails
authentication, because the ie1 account's credentials do not exist there.

This is not theoretical. It surfaced during the real Twilio provisioning for
solidping.io: the operator had created both a `us1` and an `ie1` credential
set, and only `us1` could be wired up.

Every client construction site is pinned to the default:

- [`client.go:27`](server/internal/integrations/twilio/client.go:27) —
  `DefaultBaseURL = "https://api.twilio.com"`, a const.
- [`job_escalation_step.go:33`](server/internal/jobs/jobtypes/job_escalation_step.go:33)
  and [`verify.go:66`](server/internal/handlers/usernotifications/verify.go:66)
  both do `var newTwilioClient = twilio.NewClient`, which hardcodes
  `DefaultBaseURL`. These are the two paths that matter most — escalation
  SMS/voice paging, and phone-contact verification.
- [`notifications/twilio.go:63`](server/internal/notifications/twilio.go:63)
  does call `NewClientWithBaseURL(…, s.BaseURL)`, but
  [`registry.go:45`](server/internal/notifications/registry.go:45) constructs
  `&TwilioSender{}` with no `BaseURL`, so it falls back to `DefaultBaseURL`
  too. The field is only a test seam, not a configuration surface.
- [`models.TwilioSettings`](server/internal/db/models/integration.go:532) has
  no region or base-URL field, so there is nowhere for an operator to express
  the choice in the first place.

Only the classic `/2010-04-01` REST API is used (Messages and Calls), so a
single per-connection base URL covers every outbound call SolidPing makes — no
per-product host mapping is needed.

## Proposal

### 1. A region field on the connection

Add an optional **public, non-secret** field to `TwilioSettings`:

```go
Region string `json:"region,omitempty"` // "" or "us1" (default), "ie1", "au1"
```

Prefer a short region token over a raw `api_base_url`: it is the vocabulary
Twilio itself uses in the console, it cannot be pointed at an arbitrary host by
a compromised org admin (an SSRF-ish footgun for a field that reaches an
outbound HTTP client), and it keeps the stored settings stable if Twilio ever
changes host formatting. Resolve it in the `twilio` package:

```go
func BaseURLForRegion(region string) string  // "" | "us1" → DefaultBaseURL
                                             // otherwise https://api.<region>.twilio.com
```

Validate the region against an allowlist in `validateTwilioSettings`
([integrations/service.go:501](server/internal/handlers/integrations/service.go:501)),
alongside the existing SID and E.164 checks — an unknown region must be a
`VALIDATION_ERROR` at configuration time, never a silent failure at 3 a.m.

### 2. Thread it through all three construction sites

- Change the `newTwilioClient` seam in
  [job_escalation_step.go:33](server/internal/jobs/jobtypes/job_escalation_step.go:33)
  and [verify.go:66](server/internal/handlers/usernotifications/verify.go:66)
  to take a base URL (e.g. `var newTwilioClient = twilio.NewClientWithBaseURL`),
  and pass `BaseURLForRegion(settings.Region)` at each call. The existing tests
  override these vars, so keep the seam shape test-friendly.
- In [notifications/twilio.go:63](server/internal/notifications/twilio.go:63),
  let the settings' region win over the empty `s.BaseURL`, keeping `s.BaseURL`
  as the test override. Decide the precedence explicitly and document it in a
  comment — the current fallback chain is easy to get backwards.

### 3. Callbacks

Inbound callback signature validation
([twiliocb/handler.go](server/internal/handlers/twiliocb/handler.go)) computes
HMAC-SHA1 over **our own** request URL plus the POST params, so it should be
region-agnostic. Verify this rather than assume it — confirm that a regional
account signs with the same account auth token and that nothing in the
validation path embeds a Twilio host. If it turns out region matters, the
connection is already resolved from `cid` in `VerifyMiddleware`, so the region
is available there.

### 4. Surfaces

- **dash0**: add a region selector to `TwilioPanel`
  ([integration-form.tsx:1543](web/dash0/src/components/integrations/integration-form.tsx:1543)),
  defaulting to US1. Follow the design reference as always.
- **Docs**: add a region section to
  [web/docs/docs/configuration/twilio.md](web/docs/docs/configuration/twilio.md),
  covering what data residency the choice buys, that credentials are
  **per-region** (an ie1 account has its own Account SID and auth token — a US1
  token will not work against ie1), and that the region must be chosen at
  account creation in Twilio, not switched later.

### 5. Tests

- Unit: `BaseURLForRegion` mapping, including the empty/us1 default and an
  unknown region rejected by validation.
- Prove the negative that motivated this spec: a connection with
  `region: "ie1"` must produce requests against an `api.ie1.twilio.com`-shaped
  base on **all three** paths — the escalation SMS, the escalation voice call,
  and the phone verification send. A test that only covers the notifications
  sender would pass today's code for two of the three broken paths.
- Backward compatibility: a connection with no region behaves byte-for-byte as
  today, hitting `https://api.twilio.com`.

## Open questions

- Which regions to allow? `us1` and `ie1` certainly; `au1` exists. An allowlist
  is cheap to extend, so start narrow.
- Should the region be immutable after creation? Changing it strands the
  connection's credentials (they are region-scoped), so a PATCH that changes
  region while keeping the old SID is almost certainly an operator mistake.
  Rejecting it, or at least re-validating credentials on change, is worth
  considering.

## Resolved open questions

> **Which regions to allow?** *(`us1` and `ie1` certainly; `au1` exists. An
> allowlist is cheap to extend, so start narrow.)*

**Decision: all regions are allowed — do not ship an enumerated allowlist.**

Validate the region by **format**, not by membership of a hardcoded list, so a
region Twilio adds later works without a code change:

- Accept the empty string (meaning US1, the default) or a token matching
  `^[a-z]{2}[0-9]+$` — `us1`, `ie1`, `au1`, and anything of that shape.
- Anything else is a `VALIDATION_ERROR` in `validateTwilioSettings`.

The format check is what keeps the SSRF footgun closed: `BaseURLForRegion` can
then only ever produce `https://api.<region>.twilio.com`, never an arbitrary
host, even though there is no fixed list. Keep `BaseURLForRegion`'s contract as
the spec states it — `""` and `us1` both map to `DefaultBaseURL`.

The dash0 region selector should therefore **not** be a closed dropdown of three
options. Offer the known regions (US1 / Ireland / Australia) as the common
choices but let an operator enter any valid region token, so the UI does not
become the narrow allowlist the backend deliberately avoids.

> **Should the region be immutable after creation?** *(Changing it strands the
> connection's credentials … Rejecting it, or at least re-validating credentials
> on change, is worth considering.)*

**Decision: the region stays mutable. Instead, verify the credentials against
Twilio on every write that could invalidate them.**

- Any `POST` (creation) or `PATCH` that sets or changes the **account SID**, the
  **auth token**, or the **region** must make one authenticated call to the
  resolved regional base URL before the connection is persisted. Use
  `GET /2010-04-01/Accounts/{AccountSid}.json` with basic auth — it is
  side-effect-free and is the canonical credential probe.
- Twilio rejects the credentials (401/403) → `VALIDATION_ERROR` naming the
  region, e.g. *"Twilio rejected these credentials for region ie1. Account SID
  and auth token are region-scoped — check you copied them from the ie1
  account."*
- Twilio is unreachable, times out, or 5xxs → also refuse the write, with a
  distinct message saying the credentials could not be verified. A silently
  unverified connection is exactly the 3 a.m. failure this spec exists to
  prevent.
- A `PATCH` that touches none of those three fields (renaming the connection,
  changing the from-number) must **not** trigger a verification call.

Make the verification a package-level seam (in the shape of the existing
`newTwilioClient` vars) so handler and service tests can stub it — no test may
require network access, and the existing tests that create Twilio connections
must keep passing without reaching the internet. Cover both branches: a stub
that accepts, and a stub that rejects, asserting the connection is **not**
persisted in the reject case.

## Implementation Plan

### 1. `twilio` package (`server/internal/integrations/twilio/client.go`)
- `regionPattern = ^[a-z]{2}[0-9]+$`; `ValidRegion(region string) bool` — empty
  or matches the pattern.
- `BaseURLForRegion(region string) string` — `""`/`"us1"` → `DefaultBaseURL`,
  else `https://api.<region>.twilio.com`. No validation inside — callers must
  only ever pass an already-validated region (guaranteed by
  `validateTwilioSettings` gating what gets persisted), which is what keeps
  the format check doing the SSRF-guard work described in the resolved
  question.
- `VerifyCredentials(ctx, accountSID, authToken, baseURL string) error` — GET
  `/2010-04-01/Accounts/{accountSID}.json` with basic auth, no body. 2xx → nil.
  401/403 → `ErrCredentialsRejected`. Anything else (network error, timeout,
  5xx, unexpected status) → `ErrCredentialsUnverifiable`. Both are sentinel
  errors so callers can `errors.Is`.

### 2. `models.TwilioSettings` (`server/internal/db/models/integration.go`)
- Add `Region string \`json:"region,omitempty"\`` next to the other public
  fields.

### 3. `handlers/integrations/service.go`
- `validateTwilioSettings`: reject a malformed region (`twilio.ValidRegion`)
  as `ErrInvalidSettings` (`VALIDATION_ERROR`).
- New seam `var verifyTwilioCredentials = twilio.VerifyCredentials`.
- New helper `verifyTwilioWrite(ctx, parsed *models.TwilioSettings) error`:
  skips the live call when `s.appConfig != nil && s.appConfig.RunMode ==
  "test"` (dev-test / Playwright E2E run with placeholder credentials and no
  real Twilio account — see below), otherwise resolves
  `twilio.BaseURLForRegion(parsed.Region)` and calls the seam, translating
  `ErrCredentialsRejected`/`ErrCredentialsUnverifiable` into two distinctly
  worded `ErrInvalidSettings` errors that name the region.
- `CreateIntegration`: for a Twilio connection, call `verifyTwilioWrite` after
  `validateConnectionType` and before `s.db.CreateChannel` — a POST always
  "sets" SID/token/region, so it always verifies.
- `applyUpdateSettings`: after the existing `validateTwilioSettings(merged)`
  call, if `reqSettings` (the raw incoming PATCH map) contains any of
  `account_sid` / `auth_token` / `region`, call `verifyTwilioWrite` with the
  merged settings before falling through to the encrypt/write path — a PATCH
  that touches none of the three fields must not call it at all.
- `handler.go`: `handleError` currently has no case for `ErrInvalidSettings` —
  it silently falls to `WriteInternalError` (500). Add a case mapping it to
  `400 VALIDATION_ERROR` with `err.Error()` as the message (pre-existing gap
  this spec's format/credential errors would otherwise hit).

### 4. Three client-construction call sites
- `job_escalation_step.go` / `job_escalation_step_phone_test.go` and
  `usernotifications/verify.go` / `verify_test.go`: change the
  `newTwilioClient` seam to `twilio.NewClientWithBaseURL` (3-arg: SID, token,
  baseURL) and pass `twilio.BaseURLForRegion(settings.Region)` at each call
  site (`sendPhoneSMS`, `placePhoneCall`, `sendVerificationSMS`). Update the
  two test files' seam overrides to the new 3-arg signature (they can ignore
  the passed baseURL and keep pointing at their httptest fake, since the fake
  doesn't care about region).
- `notifications/twilio.go`: precedence is `s.BaseURL` (test override) first,
  falling back to `twilio.BaseURLForRegion(settings.Region)` — documented in a
  comment. Backward compatible: empty region + empty `BaseURL` still resolves
  to `DefaultBaseURL`.

### 5. Callbacks
- `twiliocb/handler.go`: confirmed region-agnostic (signs over `cfg.Server.BaseURL`
  + POST params with the account auth token; no Twilio host anywhere in the
  path). Add a short comment recording that this was verified, not assumed.

### 6. Frontend (`integration-form.tsx` `TwilioPanel`)
- Add a `region` field: an `Input` with a `<datalist>` offering `us1` / `ie1`
  (Ireland) / `au1` (Australia) as suggestions, but accepting any typed value
  — not a closed `<Select>`, per the resolved question. Hint text: empty =
  US1, credentials are region-scoped.

### 7. Docs (`web/docs/docs/configuration/twilio.md`)
- New "Region" subsection under Step 3: what data residency buys you,
  credentials are per-region and not portable between regions, region is
  chosen at Twilio account creation (not switched later), and the connection
  now live-verifies credentials against the resolved region on save.

### 8. Tests
- `twilio` package: `BaseURLForRegion` table (empty/us1/ie1/au1/arbitrary
  well-formed), `ValidRegion` table including the SSRF-shaped rejects
  (`evil.com`, `../x`, `US1` uppercase, `us`, `1us`), `VerifyCredentials`
  against an httptest fake for the 2xx/401/timeout branches.
- `handlers/integrations` service tests: region format rejected at
  create/update; credential-verification stub accept/reject on create; PATCH
  that changes only `name` does not call the verify seam (stub panics/records
  calls to prove it); PATCH that changes `auth_token` does call it and, on
  reject, the persisted connection is unchanged.
- `jobtypes` and `usernotifications`: extend the existing fake-Twilio-server
  tests to assert the request path/host reflects
  `BaseURLForRegion(settings.Region)` for all three call sites (escalation
  SMS, escalation voice, phone verification) with a non-default region.
- `notifications` package: `TwilioSender` test proving settings' region wins
  when `BaseURL` is empty, and `BaseURL` still wins when set (existing tests
  keep passing unmodified — no region set).
