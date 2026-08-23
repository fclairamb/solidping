---
model: opus
effort: medium
---

# Custom-domain re-verification fails while DNS resolves correctly

## Problem

On `solidping-prod` (k8xp), `status.webingenia.com` carried
`custom_domain_failures = 1` while its `CNAME` resolved **correctly** to
`solidping.k8xp.com` when queried from inside a pod using the app's own
`dnsConfig`. So the re-verification sweep and a manual `dig` from the same
network namespace disagreed.

This was split out of spec `2026-08-23-03` ("Custom-domain demotion recovery and
legible TLS failure"), whose maintainer resolution explicitly scoped it out:

> **Out of scope for this spec.** [...] Investigating why re-verification fails
> against the pod's own resolver is an infrastructure/diagnostic question about
> the `solidping-prod` k8xp bring-up, not a product-code defect.

The four requirements of that spec are implemented and shipped. This one is the
residual.

## What has changed since — start from the data, not from `dig`

`2026-08-23-03` made verification failures diagnosable rather than silent. Each
re-check now records a one-line diagnostic on the status-page row and exposes it
on the authenticated API and in the dashboard:

```
custom_domain_last_check = mode=shared expected=solidping.k8xp.com resolved=… ok=false error=…
```

That line distinguishes, without shell access to a pod:

- **a mode mismatch** — `expected=` shows a `<token>.cname.<target>` host while
  the customer published the plain shared target (or the reverse);
- **an unconfigured installation** — `expected=<none: no CNAME target
  configured for this mode>`, in which case DNS was never consulted at all;
- **a resolver/transport fault** — `error=` carries the lookup error;
- **a genuine mismatch** — `resolved=` names what DNS actually returned, which
  is where CNAME-chain flattening would show up (`net.Resolver.LookupCNAME`
  returns the *canonical* final name, so an intermediate hop is invisible and a
  provider that flattens the chain answers something the check cannot match).

## What to do

1. Read `custom_domain_last_check` for the affected row(s) — dashboard, or
   `GET /api/v1/orgs/:org/status-pages/:uid` → `customDomainLastCheck`.
2. Classify the failure using the list above.
3. If the resolved value differs from what an external `dig` returns, the
   suspect is the pod's resolver path under `dnsPolicy: None` (see
   `project_k8xp_dns_localization` — node-local CoreDNS with `ndots:1` and
   FQDN-only fallback ordering, which is load-bearing).
4. Fix whatever it turns out to be, and — only if the cause is product-side —
   write the regression test at the layer that was wrong.

## Non-goals

- The demotion state machine, the grace window, re-promotion, the legible TLS
  failure and the demotion alert. All of those shipped in `2026-08-23-03`.
- Changing the verification contract (single CNAME, no dual-accept in token
  mode). If the cause turns out to be CNAME flattening, widening what verifies
  is a design decision that needs its own spec, not a fix folded into this one.
