---
model: sonnet
effort: medium
---

# A demoted custom domain never recovers, and it fails as a TLS handshake error

## Problem

`status.webingenia.com` — the acceptance-run domain for spec `2026-07-26-01` —
has been serving nothing since some point before 2026-08-23. A browser hitting
it gets a **TLS handshake failure**, not a page, not an error message:

```
$ curl https://status.webingenia.com/
curl: (35) tlsv1 alert internal error
```

Its certificate is present and valid in `tls_storage`:

```
certificates/acme-v02.api.letsencrypt.org-directory/status.webingenia.com/status.webingenia.com.crt
                                                                          …/.key
                                                                          …/.json
```

The connection does reach the pod — correlating a probe against the log
confirms it — and the edge refuses it:

```
level=WARN msg="tlsedge: refusing to serve a host we do not own" host=status.webingenia.com
level=INFO msg="http: TLS handshake error from …: tlsedge: hostname not allowed for certificate issuance: status.webingenia.com"
```

The status-page row explains why:

```
custom_domain              = status.webingenia.com
custom_domain_verified_at  = NULL          ← demoted
custom_domain_checked_at   = 2026-08-23 07:32:51+00
custom_domain_failures     = 1
```

DNS is currently **correct** — resolved from inside a pod using the app's own
`dnsConfig`, `status.webingenia.com` CNAMEs to `solidping.k8xp.com`. So the
domain is demoted while its DNS is valid and its certificate is in hand.

## Two defects, one symptom

### 1. Demotion is permanent — recovery requires a human

[`job_custom_domain_verify.go:106-127`](server/internal/jobs/jobtypes/job_custom_domain_verify.go#L106)
clears `VerifiedAt` after `customDomainReverifyMaxFailures` consecutive
failures, which is correct as release/takeover protection. But the sweep is
explicitly one-way, by its own comment:

```go
// Default: keep the existing verification state. The sweep never
// PROMOTES an unverified page — an operator has to click Verify — it
// only ever demotes one that stopped resolving.
VerifiedAt: page.CustomDomainVerifiedAt,
```

So a transient DNS fault spanning three check cycles takes a customer's status
page offline **permanently**. DNS recovering does not bring it back. Nothing
brings it back except someone noticing and clicking Verify.

Note `failures` is currently `1`, not `≥3`: this row was demoted at some earlier
point, the counter has since reset at least once, and the page is *still* dark.
That is the one-way behaviour visible in the data — a success that resets the
counter cannot undo the demotion.

The asymmetry is defensible for a domain genuinely released or transferred away.
It is indefensible for the far more common case: a blip. Takeover protection
wants to be *slow to trust again*, not *incapable of it*.

### 2. The failure mode is the worst one available

A demoted domain fails at the TLS layer. The visitor gets a full-page browser
security interstitial — the kind of warning that reads as "this site is
compromised", not "this page is misconfigured". There is no HTTP layer left to
render a message in, because the handshake never completes.

This directly contradicts the acceptance criterion the feature already claims to
meet: *"unverified / removed / expired domain degrades to a clear message, not a
TLS error page"*. It cannot be satisfied while the edge refuses the handshake —
serving *some* certificate is a precondition for saying anything at all.

Worth weighing against who sees it: a status page is what a customer shows
*their* customers during an outage. This fails silently, permanently, and is
discovered at the worst possible moment.

## Suggested direction

Not prescriptive — the shape matters more than the mechanism.

1. **Let the sweep re-promote**, under conditions stricter than first
   verification if that is the worry: N consecutive successes, or re-promote
   only while the stored certificate is still valid and the CNAME still points
   at us. A domain that is demoted, still ours by DNS, and still holding a valid
   cert is not a takeover scenario.
2. **Separate "temporarily failing" from "gone."** One counter is doing two
   jobs. A `grace` state that keeps serving while re-checks fail would make the
   common case invisible to the customer and reserve hard demotion for domains
   that have actually moved.
3. **Never fail at the handshake for a domain we have a certificate for.** If
   the cert is in `tls_storage` and unexpired, complete the handshake and answer
   with an HTTP error page. That alone converts the scariest failure mode into a
   legible one and makes the §1 criterion achievable.
4. **Alert the operator on demotion.** Today it is silent. Whatever else
   changes, a domain going dark should page someone.

## Also worth checking

`failures = 1` means re-verification is *currently* failing, even though DNS
resolves correctly from the pod's own resolver. Whatever that is — resolver
behaviour under `dnsPolicy: None`, CNAME-chain flattening, mode mismatch between
`shared` and `token` — it is a separate question from the two defects above, and
it is what keeps this particular row from healing even by hand.

## Provenance

Found 2026-08-23 while bringing up `solidping-prod` on k8xp, from outside the
product repo. The infrastructure side is recorded in
`k8xp/k8s/solidping/overlays/prod/README.md`; the business-side record is in
`solidping-business/memory/decisions.md` (2026-08-23 c). No product code was
changed.

## Resolved open questions

Answered by the maintainer on 2026-08-23. The "Suggested direction" section
above opens with "Not prescriptive"; this section makes it prescriptive.

### Q: How much of the four-item "Suggested direction" should be implemented?

**Decision: all four.** Items 1–4 are in scope for this spec and none may be
deferred. Restated as requirements:

1. **The sweep may re-promote.** A demoted domain that starts resolving to us
   again must recover on its own, without a human clicking Verify. Re-promotion
   is deliberately harder to earn than the demotion was: require N consecutive
   successful checks (not a single one), and re-promote only while the stored
   certificate is still present and unexpired and the CNAME still points at us.
   A domain that is demoted, still ours by DNS, and still holding a valid cert
   is not a takeover — treat it as a blip that ended.
2. **Split "temporarily failing" from "gone."** The single `custom_domain_failures`
   counter is doing two jobs; give the domain an explicit intermediate state
   (grace) that **keeps serving** while re-checks fail, and reserve hard demotion
   (clearing `custom_domain_verified_at`) for domains that have stayed
   unreachable well past the grace window. The common case — a blip — must
   become invisible to the page's visitors.
3. **Never fail at the TLS handshake for a domain we hold a certificate for.**
   If an unexpired cert for the SNI host is in `tls_storage`, complete the
   handshake and answer at the HTTP layer with a legible error page. The edge's
   current "refusing to serve a host we do not own" path must stop being reachable
   whenever we actually have the cert. This is what makes the existing acceptance
   criterion from spec `2026-07-26-01` ("unverified / removed / expired domain
   degrades to a clear message, not a TLS error page") satisfiable at all — treat
   that criterion as binding and cover it with a test.
4. **Alert the operator on hard demotion.** A custom domain going dark must
   notify, through the org's existing notification routing, rather than being
   discovered by a customer during an outage. Grace-state entry need not page;
   the hard demotion must.

### Q: the "Also worth checking" item (`failures = 1` while DNS resolves correctly)

**Out of scope for this spec.** The spec itself calls it "a separate question
from the two defects above". Investigating why re-verification fails against the
pod's own resolver is an infrastructure/diagnostic question about the
`solidping-prod` k8xp bring-up, not a product-code defect, and the four
requirements above are all implementable and testable without resolving it.

It should not be lost, though: the implementer should make the verification
failure **diagnosable** — when a check fails, record enough about *why* (what was
resolved, by which mode, what was expected) that this question can be answered
from the data next time instead of by correlating pod logs with manual `dig`
runs. File the residual investigation as its own spec.
