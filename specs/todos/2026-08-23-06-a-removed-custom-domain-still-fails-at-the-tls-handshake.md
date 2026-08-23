---
model: opus
effort: high
---

# A *removed* custom domain still fails at the TLS handshake

## Problem

Spec `2026-07-26-01` claims this acceptance criterion:

> unverified / removed / expired domain degrades to a clear message, not a TLS
> error page

Spec `2026-08-23-03` made two thirds of it true. A domain that is **unverified**
— demoted by the re-verification sweep — now completes the handshake with the
certificate still held for it and answers `503` with a legible page. The
**expired** third is genuinely unachievable and correctly left alone: an expired
certificate cannot complete a handshake without a browser interstitial no matter
what we do, which is why `server/internal/tlsedge/demoted_host_test.go` pins the
refusal for it.

The **removed** third is still unmet, and — unlike "expired" — it is unmet by
*policy*, not by physics.

When an operator clears or re-points a custom domain, we delete its certificate
and private key:

- `server/internal/handlers/statuspages/custom_domain.go:265` (re-point) and
  `:284` (clear) call `forget`
- `server/internal/handlers/statuspages/service.go:400-409` states the reason
- `server/internal/tlsedge/edge.go:308` `ForgetDomain` removes the material from
  `tls_storage`

With nothing in storage, `Edge.TLSConfig`'s `GetCertificate` correctly falls
through to `ErrHostNotAllowed`, and a visitor whose DNS still points at us gets
`curl: (35) tlsv1 alert internal error` — the browser security interstitial that
reads as "this site is compromised" rather than "this page was removed".

Retaining a legitimately issued certificate until its natural expiry is a real
option that we decline for a stated security reason. That makes this a design
decision to revisit, not an oversight to patch.

Note what this spec is NOT claiming: `2026-08-23-03` met **its own** requirement
3 as worded — *"if an unexpired cert for the SNI host is in `tls_storage`,
complete the handshake"*. The residual is the inherited criterion's "removed"
clause only.

## Two things that are true and one that is not

**True:** serving a *bogus* certificate would be worse than refusing. **But that
answers a different question.** It does not justify deleting a *valid* one.

**True:** a private key for a hostname that may now belong to someone else is a
liability. That is the whole trade-off, stated below.

**Not true:** that fixing the TLS half alone would help. It would make things
**worse**. Once the domain is cleared from `status_pages`,
`lookupCustomDomain` (`server/internal/app/custom_domain_routing.go:212`)
returns `known=false`, and the request falls through to the instance's own-host
routing (`:145`) — serving the **operator dashboard** on a customer's hostname.
That is precisely the bug requirement 3's HTTP half exists to stop. Completing
the handshake without a matching record would re-open it.

## Shape

Two parts. Neither works alone.

### 1. A bounded retention window for the certificate

Do not delete the material at removal. Mark it retained-until-`T` and let the
existing cleanup drop it at `T`. Inside the window the handshake completes; past
it, the domain refuses exactly as today. The window is what bounds the exposure
the current policy eliminates outright.

### 2. A tombstone record for the hostname

A removed domain needs to stay **recognisable** even though it is no longer
mapped, so `lookupCustomDomain` can answer `known=true` and route to the
existing legible `503` instead of falling through. A tombstone row (hostname +
removed-at + retention deadline, no page binding) is the minimum. It must
**not** be a mapping: it grants no serving rights, and the global unique index
on `status_pages.custom_domain` must stay the arbiter, so another org can still
claim the hostname immediately and win.

Both parts must land together, or the change is a regression.

## The security trade-off, stated plainly

Today: **no live private key exists for a hostname we no longer serve.** That is
a clean, absolute property, and it is the reason `ForgetDomain` deletes rather
than expires.

With retention: for the length of the window, we hold a valid certificate and
private key for a hostname that may have been transferred to someone else. We
would use it only to say "this page was removed" — but we would *hold* it, and
"we only use it for X" is not a property a compromised process preserves.

What we buy: the removed case stops producing a browser security interstitial on
a hostname a customer may still be pointing at us, which is what
`2026-07-26-01`'s criterion asked for.

## Open questions

**These must be answered by the maintainer before implementation starts.**

1. **Do we accept holding a private key for a hostname that may have been
   transferred away?** If the answer is no, this spec should be closed and
   `2026-07-26-01`'s criterion amended to scope out "removed" — which is a
   legitimate outcome, and better than leaving a criterion on the books that we
   have decided not to meet.

2. **If yes, how long is the retention window?** It bounds the exposure and it
   bounds the usefulness, in opposite directions. Candidates: 7 days (long
   enough for a customer to notice and fix DNS, short enough to be a footnote),
   30 days, or "until natural expiry" (up to 90 days — maximum usefulness,
   maximum exposure).

3. **Does an explicit removal differ from a re-point?** Re-pointing a page to a
   new hostname leaves the old one unmapped for a different reason: the operator
   is mid-migration and may well still have traffic arriving on the old host.
   That case arguably wants retention more than a deliberate removal does.

4. **Should retention be operator-controllable?** A "delete the certificate
   immediately" action on removal gives the security-conscious installation the
   current behaviour back without denying it to everyone else.

## Acceptance criteria

- A domain removed by an operator, whose DNS still points at the installation
  and whose certificate is inside the retention window, completes the TLS
  handshake and receives the legible `503` — proved by a test that completes a
  real handshake over a live listener and asserts an HTTP response, in the shape
  of `TestDemotedHostWithStoredCertificateAnswersOverHTTP`.
- The same domain **never** reaches the instance's own-host routing. A test must
  assert the operator dashboard is not served on it.
- Past the retention window, the handshake is refused again — a negative control
  proving the window is real and not effectively infinite.
- A hostname claimed by another organization after removal is served by the NEW
  owner, not by the tombstone. The tombstone must lose to a live mapping.
- `web/docs/docs/features/custom-domains.md` states the retention window and
  what is held during it. This is a security-relevant behaviour change for
  self-hosters and must not be discovered from the source.

## Provenance

Split out of `2026-08-23-03` during its completeness audit (2026-08-23), which
assessed the implementer's self-flagged residual and concluded it is a genuine
gap requiring a design decision rather than a patch.

## Resolved open questions

**Maintainer decision, 2026-08-23: this spec is CLOSED — retention is declined.**
Do not implement it. The file is archived under `specs/cancelled/` for the record.

> 1. **Do we accept holding a private key for a hostname that may have been
>    transferred away?**

**No.** Today's property — *no live private key exists for a hostname we no
longer serve* — is clean and absolute, and we keep it. `ForgetDomain` continues
to delete the certificate and private key at removal or re-point. The bounded
retention window and the tombstone record are both dropped; neither part is to
be built, since the spec itself states that either one alone is a regression.

> 2. **If yes, how long is the retention window?**
> 3. **Does an explicit removal differ from a re-point?**
> 4. **Should retention be operator-controllable?**

**Moot.** All three are conditional on question 1, which was answered no.

### Consequence for `2026-07-26-01`'s inherited criterion

The criterion *"unverified / removed / expired domain degrades to a clear
message, not a TLS error page"* is deliberately **not met for the "removed"
clause**, and that is now the recorded position rather than an outstanding gap:

- **unverified** — met by `2026-08-23-03`: the handshake completes with the
  certificate still held and the visitor gets a legible `503`.
- **expired** — unachievable by physics; pinned by
  `server/internal/tlsedge/demoted_host_test.go`.
- **removed** — **out of scope by decision.** A removed domain holds no
  certificate, so the handshake is refused and a visitor whose DNS still points
  at the installation sees a browser TLS warning until they re-point it. This is
  the accepted cost of not retaining private keys for hostnames we no longer
  serve.

Note on where to record this: the quoted criterion does not appear verbatim in
`specs/done/2026/07/2026-07-26-01-custom-domain-cname-only-internal-acme.md` —
it is a paraphrase introduced by `2026-08-23-03`, which declared it binding.
There is therefore no criterion line to strike; this closure record is the
amendment. The user-facing behaviour is already documented accurately in
`web/docs/docs/features/custom-domains.md` (clearing or changing a domain
deletes its certificate and private key; a hostname we hold no certificate for
is refused outright at the handshake), so no documentation change is required.
