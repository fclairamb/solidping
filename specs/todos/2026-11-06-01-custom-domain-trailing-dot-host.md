---
model: opus
effort: medium
pr: batch
automerge: false
---

# A custom status domain spelled with a trailing dot serves the dashboard

## Problem

Found by the security scan of 2026-11-06 (finding AF1). Severity: low. Anyone can trigger it without authentication, by sending a status-page visitor a link to the dotted hostname.

The rule (`server/internal/app/custom_domain_routing.go:34-38`, `:44-48`) is that a customer's status domain must never serve or redirect into the SolidPing dashboard. The host router checks this with exact string matches on a Host that still carries the FQDN trailing dot:

- `hostOnly` (`custom_domain_routing.go:192-200`) strips the port and lowercases, but keeps a trailing `.`.
- `handlerWithCustomDomains` (`:165-189`) passes that host to `isReservedCustomHost` (`:205-213`) and to `resolveCustomDomain` → `GetStatusPageByCustomDomain` (`db/postgres/postgres.go:5782`, `db/sqlite/sqlite.go:5740`). Both are exact `=` matches.
- Stored domains have no dot: `domainverify.Normalize` strips it (`domainverify/verifier.go:107-109`).

So a request for `status.acme.com.` misses the custom-domain row. It is not "known but unservable" either, so it falls through to `next`, the instance's own routing: dash0 login, `/docs`, the full API. All of that sits under a hostname that reads as the customer's status domain.

TLS does not stop this. Browsers send SNI without the dot, and the edge's on-demand certificate check normalizes the dot away: `CustomDomainServable` (`handlers/statuspages/custom_domain.go:432-436`) and the in-process edge's `normalizeHost` (`tlsedge/edge.go:721-723`). The certificate for `status.acme.com` is presented, and only the HTTP routing layer disagrees.

Impact: phishing and brand confusion. A link on the customer's own domain lands on a SolidPing sign-in page. No privilege is gained: authentication still applies, and the dotted name is its own browser origin, so no status-page or dashboard storage is shared. The same gap exists for the base-URL host and `docsHostMatches` (`app/server.go:3050-3057`). There it is cosmetic (the dotted docs host does not redirect into `/docs`).

## Proposal

1. Strip one trailing dot in `hostOnly` (`custom_domain_routing.go:192-200`), after the port split, the same way `tlsedge.normalizeHost` does. Every custom-domain decision (reserved check, cache key, DB lookup, the unavailable page) then uses the canonical name.
2. Apply the same normalization in `docsHostMatches` (`app/server.go:3050-3057`), so the docs host and its dotted spelling behave alike.
3. Leave the `Host` header itself untouched: OG meta (`status0_meta.go:105`) already escapes it.

## Tests

- `server/internal/app/custom_domain_routing_test.go` `TestHostOnly`: `hostOnly("status.acme.com.")` and `hostOnly("status.acme.com.:443")` equal `"status.acme.com"`. This fails before the fix.
- `TestHandlerWithCustomDomains`: add a case with `Host: status.acme.com.` against the seeded verified domain. It must reach the status shell, never `next`; dash0 paths get the custom-host 404/deny. Fails before the fix: the request reaches `next`.
- Positive control in the same table: an undotted `status.acme.com` still serves the status page, and an unknown host (`other.example.com.`) still falls through to `next`.
- Reserved host: the base-URL host with a trailing dot is treated as reserved (falls through to `next` without a DB lookup).
