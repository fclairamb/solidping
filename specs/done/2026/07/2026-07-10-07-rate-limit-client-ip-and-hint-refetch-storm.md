# Rate limiting: bogus client-IP identification + hint-driven dashboard refetch storm → 429 for innocent users

## Problem

On https://solidping.k8xp.com a user browsing the `test` org's check detail page
(portrait.sh) got the generic error card with detail **"Rate limit exceeded,
please slow down"** — a 429 from the per-IP rate limiter
(`server/internal/middleware/ratelimit.go:214`) — without doing anything
unusual. Diagnosed live on 2026-07-10 (~13:45–14:05 UTC); three compounding
causes, one still latent.

### 1. All clients share one rate-limit bucket (active outage cause)

The dev deployment sets no rate-limit env vars, so it runs the defaults
(`server/internal/config/config.go:660`): 300 req/min, burst 60,
`TrustedProxies: 0`. Behind the traefik ingress, `extractIP`
(`ratelimit.go:175`) then ignores `X-Forwarded-For` and buckets by
`RemoteAddr` — traefik's cluster IP — so **every external client shares a
single 300 rpm bucket**. Verified: a fresh `curl` to `/api/mgmt/limits`
reported `callerRemaining: 0` (a fresh IP should see ~60 burst tokens), and
the backend log showed every `/api/v1` request delayed ~1.5–1.9 s — the
slow-lane queue (`RateLimit` middleware, `ratelimit.go:239`) waiting on
token refills because the shared bucket is permanently empty.

### 2. `extractIPFromXFF` is off by one (latent, defeats the fix above)

`extractIPFromXFF` (`ratelimit.go:165`) computes
`idx := len(parts) - trustedProxies` and returns `parts[idx-1]`. The real
client is at `parts[idx]` (0-indexed): each proxy hop appends *its peer's*
IP, so with N trusted hops the client is the Nth entry from the right.
Consequences (reproduced in isolation with an exact copy of the function):

- `extractIPFromXFF("82.124.196.212", 1)` → `""` — the honest single-entry
  case behind one proxy. Falls through to `X-Real-IP`/`RemoteAddr`, so even
  with `TrustedProxies=1` correctly set, per-client bucketing only works if
  the proxy sets `X-Real-IP` (traefik does; AWS ALB and default nginx don't
  → everyone still collapses into the shared `RemoteAddr` bucket).
- `extractIPFromXFF("6.6.6.6, 82.124.196.212", 1)` → `"6.6.6.6"` — behind a
  proxy that *appends* rather than strips client XFF (ALB, default nginx),
  the client-supplied entry is returned: **an attacker picks their own
  bucket, i.e. a full rate-limit bypass**.
- The `X-Real-IP` fallback (`ratelimit.go:182`) is trusted whenever
  `TrustedProxies > 0`, which is equally spoofable behind proxies that don't
  strip/overwrite it.

`TestExtractIP_TrustedProxies` (`ratelimit_test.go:179`) never catches any
of this: it sends 2 requests against a burst of 5, so it passes even with
extraction completely broken (both requests silently land in the same
`RemoteAddr` bucket).

Empirically on this traefik: client-supplied XFF *is* stripped (a login sent
with a forged `X-Forwarded-For: 203.0.113.99` recorded the real egress IP in
the session's `createdWith.remoteAddr`), so the spoof path is closed *on this
infra* — but the single-entry → `""` path is exactly what every honest
request hits.

### 3. Live-hint invalidation turns each dashboard tab into ~4 req/s (the traffic source)

What actually drains the bucket: the org dashboard's live-updates scope
refetches everything on every hint.

- Server coalesces hints per org per **1 s** window
  (`RealtimeConfig.FlushInterval`, `config.go:169`).
- Client `invalidateScope` calls `queryClient.invalidateQueries` on **every**
  `onUpdate` hint with no debounce
  (`web/dash0/src/contexts/LiveEventsContext.tsx:242`).
- The dashboard page rides three heavy queries on the `checks` scope
  (`web/dash0/src/components/dashboard/dashboard-page.tsx:239`):
  `checks?with=last_result,last_status_change&limit=1000` (~45 KB),
  `results?periodType=day&limit=1000`, `results?periodType=hour&limit=1000`.

In an org whose checks produce results roughly every second (acmetech,
multiple regions), every 1 s flush carries a `checks` hint → each open
dashboard tab refetches ~4 heavy queries **per second, indefinitely** — worse
than the 30–60 s polling live mode replaced. Observed: ~3,100 API requests /
10 min on the pod (`/results` 1,323×, `/checks` 1,203×), steady ~1 s cadence
per tab from 3 client IPs whose WebSockets were healthy and long-lived (so
not a reconnect loop — this is the *designed* hint path).

### Aggravating factor: the team shares one egress IP

The office/VPN egress is a single AWS-Paris IP (13.37.139.219 — verified as
this machine's own public IP and one of the storm clients). Even with
perfect per-IP identification, everyone on the VPN lands in **one** bucket,
so a couple of open dashboard tabs rate-limit the whole team.

## Proposal

1. **Fix `extractIPFromXFF`** (`ratelimit.go:165`): clamp
   `idx := len(parts) - trustedProxies` to `>= 0` and return `parts[idx]`
   (TrimSpace kept; `idx >= len(parts)` → fall back to `parts[0]` or `""`).
   Gate the `X-Real-IP` fallback to the XFF-absent case and document the
   proxy-must-overwrite assumption. Strengthen the tests: exhaust one XFF
   identity's bucket and assert a different XFF identity still passes, plus
   direct unit tests of `extractIPFromXFF` for 1-entry / 2-entry / spoofed
   inputs.

2. **Damp hint-driven refetches client-side**
   (`LiveEventsContext.tsx`): per-scope minimum invalidation interval (a few
   seconds, trailing-edge so the last hint still lands), and/or filter by
   hint `kinds` so a result-only hint doesn't refetch the full checks
   membership list. Keep the freshly-created-check freshness path
   (`checks.$checkUid.index.tsx` fast poll + `onSubscribed` catch-up
   invalidation) intact.

3. **Key authenticated traffic by user/token, not IP.** Per-IP is the wrong
   identity for logged-in dashboard traffic behind a shared VPN/NAT egress.
   Bucket authenticated requests per token/user (IP bucketing stays for
   anonymous traffic), or at minimum give authenticated sessions a separate,
   higher tier.

4. **Ops (separate repo, for the record):** set
   `SP_RATE_LIMITING_TRUSTED_PROXIES=1` on the k8xp solidping deployments so
   the backend reads the traefik-set headers at all. Only effective for
   honest clients once (1) lands (today it would ride on the `X-Real-IP`
   fallback alone).

## Open questions

- Should `/api/mgmt/limits` also echo the resolved caller IP to make future
  diagnosis one curl instead of a log dive?
- Server-side, is 1 s the right `FlushInterval` floor for collection scopes,
  or should busy orgs coalesce `checks`-scope hints over a longer window
  (e.g. 5 s) independently of per-check scopes?

## Implementation Plan

Scope: Proposal items 1–3 plus the `/api/mgmt/limits` caller echo (open
question 1). Item 4 (k8xp env var) is out of scope — separate repo, handled
elsewhere. No server-side `FlushInterval` change (client damping is the fix).

### Step 1 — Fix `extractIPFromXFF` + gate `X-Real-IP` (backend)

`server/internal/middleware/ratelimit.go`:

- `extractIPFromXFF`: the client is the Nth entry **from the right** with N
  trusted hops, i.e. `parts[len(parts)-trustedProxies]` 0-indexed. Clamp the
  index to `>= 0` (when `trustedProxies >= len(parts)`, the left-most entry
  is the best available answer — return `parts[0]`). Keep `TrimSpace`.
- `extractIP`: consult `X-Real-IP` **only when `X-Forwarded-For` is absent**
  (a present-but-unparsable XFF falls through to `RemoteAddr`, not to the
  equally-client-controllable `X-Real-IP`). Document the assumption that the
  trusted proxy strips/overwrites client-supplied `X-Real-IP`.
- Tests: new `ratelimit_internal_test.go` (same package) unit-testing
  `extractIPFromXFF`/`extractIP` directly: 1-entry, 2-entry (spoofed left
  entry ignored), 3-entry at `trustedProxies` 1/2/3, clamp case, whitespace,
  X-Real-IP gating, `TrustedProxies=0` → RemoteAddr. Strengthen
  `TestExtractIP_TrustedProxies` in `ratelimit_test.go`: exhaust one XFF
  identity's bucket (burst 2, no rate queue → 3rd request 429) and assert a
  different XFF identity from the same `RemoteAddr` still passes.

### Step 2 — Per-token bucket keying for authenticated traffic (backend)

Problem: the limiter runs BEFORE auth in the middleware chain
(`server.go` `SetupRoutes`), so no verified claims exist yet. Design: key the
bucket by a **cheap stable hash of the presented bearer token** (SHA-256,
first 16 hex chars, `t:` prefix) when one is present — reusing the package's
`extractToken` (Authorization header, cookie fallback) — falling back to the
client IP for anonymous requests.

Why keying by *unverified* token is sound for rate limiting: the goal is
fair-share isolation between legitimate users behind one NAT/VPN egress, not
authentication. The obvious abuse (minting random tokens to obtain unlimited
fresh buckets, bypassing the per-IP limit) is mitigated by capping the number
of live token buckets per client IP: a new config knob
`token_buckets_per_ip` (`SP_SERVER_RATE_LIMITING_TOKEN_BUCKETS_PER_IP`,
default 50, 0 = disable token keying entirely). Once an IP has that many
distinct live token buckets, further unseen tokens from that IP fall back to
the shared per-IP bucket. Worst case a single IP can obtain (cap+1)× the
per-IP allowance — bounded and configurable; legitimate teams behind one
egress stay isolated per user. Per-IP token registries expire on the same
5-minute idle window as the buckets themselves (cleanupLoop).

Both `RateLimit` and `ConcurrencyLimit` use the same bucket key.

Tests: two tokens from one IP get independent buckets; anonymous traffic from
that IP keeps its own; the same token from two IPs shares one bucket; cap
overflow falls back to the IP bucket; `token_buckets_per_ip=0` restores pure
IP keying.

### Step 3 — `/api/mgmt/limits` caller echo (backend)

`server/internal/app/server.go` `getLimits`: report the state of the
caller's **actual** bucket (token bucket when a token is presented), and echo
`callerIp` (resolved client IP) and `callerBucket` (`"ip"` | `"token"`) in
the response, so future diagnosis is one curl. New
`RateLimiter.CallerBucket(req)` helper. Handler test for both shapes.

### Step 4 — Client-side hint damping (dash0)

`web/dash0/src/contexts/LiveEventsContext.tsx`:

- New exported `LIVE_INVALIDATE_MIN_INTERVAL_MS = 3000`.
- `LiveRegistry` keeps a per-scope-key damper: `onUpdate` hints invalidate
  immediately when the scope hasn't invalidated within the interval;
  otherwise they merge their `kinds` into a pending set and schedule exactly
  one **trailing-edge** deferred invalidation at cooldown expiry (so the last
  hint of a burst always lands). Empty `kinds` (= all) subsumes the pending
  set.
- `onSubscribed` catch-up and `onResync` invalidations stay immediate
  (one-shot events, not part of the storm); they reset the scope's cooldown
  and clear any pending deferred work (they invalidate everything anyway).
- `stop()` clears all pending timers.
- The freshly-created-check fast-poll path (`checks.$checkUid.index.tsx`)
  is untouched.

Tests (`LiveEventsContext.test.ts`, fake timers): first hint immediate; burst
during cooldown → exactly one deferred invalidation at expiry with merged
kinds; hint after a quiet period immediate again; `onSubscribed` immediate
during cooldown; `stop()` cancels pending work.

### Step 5 — QA

`make build-backend lint-back test`; `make build-dash0` + `cd web/dash0 &&
bun run lint` (no NEW eslint errors) + `bun run test:unit`; run the
live-updates Playwright specs against a test-mode side-car server if
feasible, otherwise report authored-but-not-run.
