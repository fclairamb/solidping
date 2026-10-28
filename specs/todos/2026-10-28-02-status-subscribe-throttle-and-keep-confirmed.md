---
model: opus
effort: medium
---

# Public status-page subscribe sends unthrottled confirm emails and un-confirms existing subscribers

## Problem

Found by the security scan of 2026-10-28 (finding X2).

**Severity: medium.** Anyone can trigger it, with no account. It works against any public status page, including one the attacker created in their own org.

`POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers` is public (`server/internal/app/server.go:2259`). The comment above it says the global per-IP limit applies, and that "double opt-in is the primary anti-abuse control". That limit is 1800 requests per minute by default (`server/internal/config/config.go:1899`). Double opt-in does not hold for three reasons:

1. **The confirm email is sent on every request, with no per-recipient or per-page budget.**
   - The handler sends it synchronously after each successful `Subscribe` (`server/internal/handlers/statussubscribers/handler.go:73-95`).
   - Re-subscribing the same address takes the `FindAnySubscriber` → `ResubscribeSubscriber` path, which rotates the tokens and returns success, so the email goes out again (`service.go:288-313`).
   - Nothing in `internal/email` throttles either.
   - Result: any third-party address can be flooded with confirm emails sent from the instance's own mail domain, at up to the per-IP rate, from several IPs. On SaaS that burns the sender reputation every customer depends on.
   - The password-reset flow guards against the same thing with per-user and per-IP counters (`server/internal/handlers/auth/service.go:3075-3088`). E2 (spec 2026-10-16-13) treated a race on that cap as a bug.
2. **Re-subscribing silently cancels a confirmed subscription.**
   - `ResubscribeSubscriber` sets `confirmed_at = NULL` and replaces the unsubscribe token on any existing row, confirmed ones included (`server/internal/db/sqlite/status_page_subscriber.go:150-163`, same in `db/postgres`).
   - The fan-out reads only `confirmed_at IS NOT NULL` rows (`sqlite/status_page_subscriber.go:198-207`).
   - So an anonymous caller who knows or guesses a subscriber's address (`ops@acme.com`) can stop that subscriber's incident notifications. The subscriber only gets an unexpected "confirm your subscription" email, and the unsubscribe links in their earlier emails stop working.
3. **Unconfirmed rows never expire.** Each distinct address inserts a row that no job removes (`server/internal/jobs/jobtypes/job_state_cleanup.go` does not touch this table), and the confirm token has no expiry. The table grows without bound from anonymous traffic.

## Proposal

1. **Keep a confirmed subscription confirmed.** In `upsertSubscriber` (`server/internal/handlers/statussubscribers/service.go:288`), when the existing row is live (`deleted_at IS NULL`) and confirmed:
   - return success without changing the row and without sending an email;
   - the response stays the same `202 pending_confirmation`, so the endpoint does not reveal whether an address is subscribed.

   Revive and re-confirm only soft-deleted or unconfirmed rows.
2. **Budget the confirm email** with `BumpGlobalCounter` (the primitive the auth counters use, `service.go:3069-3088`):
   - per recipient address, across all pages: e.g. 3 per hour;
   - per page: e.g. 100 per hour;
   - per client IP, resolved with `base.ExtractTrustedClientIP`, never the spoofable `ExtractRemoteAddr` (known M10).

   Over budget, keep answering 202 but send nothing, so the endpoint does not become an oracle either.
3. **Expire unconfirmed rows.** Add an expiry for the confirm token (e.g. 7 days, checked in the confirm lookup). Have `StateCleanupJob` hard-delete unconfirmed rows older than that.
4. Update the route comment at `server.go:2250-2258` to describe the real controls.

## Tests

- `server/internal/handlers/statussubscribers` (both engines):
  - **Regression (fails before the fix):**
    - subscribe and confirm an address, then subscribe it again through the public endpoint. The row stays confirmed, the unsubscribe token is unchanged, and no email is sent.
    - 10 subscribe calls for the same new address send at most the per-recipient budget of emails. Every call still answers 202.
  - **Positive controls:**
    - a first subscribe sends exactly one confirm email;
    - a soft-deleted (unsubscribed) address can subscribe again and receives a new confirm email.
  - An unconfirmed row older than the expiry is removed by the cleanup job. Its confirm link is then refused. A confirmed row of the same age is kept.

## Open questions

- Budgets: **recommended** 3 per recipient per hour, 100 per page per hour, 20 per IP per hour. Make them config values with those defaults.
