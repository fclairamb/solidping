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
