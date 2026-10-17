---
model: opus
effort: high
---

# Public check tools: one anonymous, guarded, rate-limited "run once" endpoint for www.solidping.io/tools

## Problem
The adoption bottleneck is rung 1: people don't find SolidPing. The directory and awesome-list lanes are exhausted at the current star count (about 190 listings, signups flat). Search is the channel left that needs nobody's permission, and the queries with steady demand in this niche are tools, not products: "website down checker", "is my site down", "SSL certificate checker", "check SSL expiry", "DNS lookup", "port checker", "check if port is open". Each of these is exactly one SolidPing check, run once.

The website can't offer them today because the server has no anonymous way to run a check:
- `POST .../checks/:check/run-now` (`server/internal/app/server.go:1443`) needs an existing check in an org, behind auth.
- `.../checks/validate` validates a config without running it.
- The public endpoints are read-only (`/check-types`, `/check-types/samples`, `/regions`, status pages, badges, heartbeats).

Running a check on behalf of an anonymous visitor is a server-side request to a target the visitor chooses, so the hard part is the guard, not the endpoint. The guard already exists: `server/internal/egress` resolves once, refuses every non-public answer (loopback, RFC 1918, CGNAT, link-local including `169.254.169.254`, ULA, multicast, reserved), dials the pinned IP and re-checks the socket address in a `Control` hook, so DNS rebinding can't slip through. It is wired into the shared transports in `checkers/checkerdef` (`outbound.go`, `httptransport.go`) and switched off only by `SP_EGRESS_ALLOW_PRIVATE` / `egress.allow_private_targets`. This endpoint must use it, and must ignore that switch.

The website half (pages, copy, CTAs) is a separate spec in `solidping-website`. This one is the server contract it calls.

## Proposal

### 1. Endpoint
`POST /api/v1/tools/run`, public, no auth, JSON body:

```json
{ "type": "http", "target": "https://example.com", "options": { "record": "A" } }
```

- Types in v1: `http` (GET only), `ssl` (TLS handshake + certificate), `dns` (A, AAAA, CNAME, MX, TXT, NS, CAA), `tcp` (connect to host:port).
- Built from the existing checker `config` packages (the light ones the `sp` CLI uses) plus the checker implementation, executed once in process. No check row, no result row, no incident, no notification, nothing persisted.
- Visitor-controlled input is reduced to the target plus a few safe options. No custom headers, bodies, methods, auth, scripts, browser checks or credentials. HTTP follows at most 5 redirects and reads at most 1 MiB of body. Timeout 10 s per run.
- Disabled unless `SP_TOOLS_ENABLED=true` (system parameter `tools.enabled`, default `false`). A self-hoster never exposes an anonymous probe by accident; the hosted solidping.io turns it on.

### 2. Network guard (mandatory, before anything ships)
- Every run goes through `egress.New(false)` attached with `egress.WithGuard(ctx, g)`, **hardwired to deny private targets**. `SP_EGRESS_ALLOW_PRIVATE=true` (set on an instance that monitors its own LAN) must not open the anonymous endpoint to the LAN too. A test pins this.
- Check that each v1 type actually dials through the guarded transport: `http` and `ssl` through `checkerdef/httptransport.go`, `tcp` through `checkerdef/outbound.go`. `dns` is the odd one: a resolver query to a public resolver is fine, but a user-chosen nameserver option is out of scope in v1 (no `server` option).
- Redirect hops: confirm the guarded transport re-checks each hop (it dials per hop, so it should). Add a test either way.
- Also refuse SolidPing's own public hosts (config list), so the endpoint can't be pointed back at our own API through a public name.

### 3. Abuse limits
- Per client IP: 10 runs a minute, 100 a day (the existing `middleware.RateLimiter` with a stricter config, like `deviceConsentRateLimitConfig` at `server.go:593`). Global concurrency cap with a semaphore (start at 16) and a 503 with `Retry-After` past it.
- Per target: cache the normalised result for 60 s keyed by `(type, target, options)`. A hundred visitors checking the same site cost one request to it, and the endpoint can't be used to hammer a third party.
- `tcp` rejects port 0 and ports above 65535 and is subject to the same per-IP limits, which keeps port-scanning through us slow and visible. Log per-IP-hash counters so abuse is easy to spot. Logs never carry the raw IP.

### 4. Response
One normalised shape for every type:

```json
{
  "type": "ssl",
  "target": "example.com:443",
  "status": "up",
  "duration_ms": 182,
  "checked_at": "2026-10-14T10:00:00Z",
  "region": "eu-paris",
  "details": { "issuer": "...", "not_after": "...", "days_left": 41, "sans": ["..."], "chain_valid": true },
  "monitor": { "check_type": "ssl", "query": "checkType=ssl&..." }
}
```

- `details` per type: `http` gives the status code, final URL, redirect chain, response time and certificate expiry when HTTPS. `ssl` gives the issuer, validity dates, days left, SANs, chain validity and TLS version. `dns` gives the records found. `tcp` gives connected or not, and connect time.
- `monitor.query` is the query string `checks.new` already understands (`web/dash0/src/routes/orgs/$org/checks.new.tsx:71`: `checkType`, `checkName`, `httpUrl`…), so www can turn a one-off result into "monitor this every minute" with the form prefilled.
- Errors are explicit and safe to show: `target_not_allowed` (guard), `rate_limited`, `timeout`, `resolve_failed`, `invalid_target`.

### 5. CORS and analytics
- Add `/api/v1/tools/` to `isPublicCORSPath` (`server.go:2608`) so www can call it from the browser.
- Server-side PostHog event `tool_run` with `{type, status, cached}` only. No target, no IP: a URL a visitor checks is their business.

### 6. Later, not v1
`"regions": "all"` runs the same check from every cloud region in parallel and answers "is it down for everyone or just me", which no single-location checker can. The placement code exists (`server/internal/regions/placement.go`). Ship v1 single-region first, then measure.

## Tests
- Guard wiring test per type, using `egress.WithLookup` to fake DNS: a name resolving to `127.0.0.1`, to `169.254.169.254`, to one public and one private address, and a rebinding resolver (public then private), all answer `target_not_allowed`. Numeric hosts (`2130706433`, `::ffff:127.0.0.1`) too, via `egress.ParseURLHost`. The `egress` package already has its own range tests; these test that the endpoint uses it.
- With `SP_EGRESS_ALLOW_PRIVATE=true`, the tools endpoint still refuses `127.0.0.1`.
- HTTP redirect to `http://169.254.169.254/` is rejected at the hop.
- Rate limit: the 11th run in a minute from one IP gets 429. Cache: two identical runs inside 60 s make one outbound request.
- No rows are written: count `checks`, `results`, `incidents` before and after a run.
- `tools.enabled=false` returns 404, and CORS headers are present on `/api/v1/tools/run`.

## To verify
- On staging with `SP_TOOLS_ENABLED=true`: `curl` an HTTP, SSL, DNS and TCP run against public targets, then `http://localhost`, `http://169.254.169.254/latest/meta-data/`, a public name with a private A record, and a redirect to a private address. All four must answer `target_not_allowed`.
- Burst 20 requests from one IP: 10 succeed, 10 get 429.

## Open questions
1. Run inline on the API node or dispatch to a worker? Recommended: inline with the semaphore for v1 (simple, 10 s budget), then move to workers when `regions: all` lands.
2. Should `tcp` be limited to a port allowlist (80, 443, 22, 25, 3306, 5432, 6379, 8080…)? Recommended: no allowlist, per-IP limits instead. "Is my port X open" with an arbitrary port is the query people search for.
