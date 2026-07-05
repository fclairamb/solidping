# Heartbeat pings: capture caller metadata (User-Agent, source IP, method)

## Problem

Heartbeat is the one check type where SolidPing is not the caller — an
external cron job / batch process / app pings SolidPing instead
(`POST /api/v1/heartbeat/:org/:identifier`, also accepts `GET`, registered
`server/internal/app/server.go:711-715`). The handler
(`server/internal/handlers/heartbeat/handler.go:35-55`) reads `org`,
`identifier`, `token`, `status`, and an optional JSON `message` body, and the
service (`service.go:85-175`) persists one `models.Result` row per ping with
`Output: models.JSONMap{"message": outputMessage}`
(`service.go:141-153`).

Nothing about the *caller* is captured — no User-Agent, no source IP, no
request method. For every other check type SolidPing is the client and
already knows exactly what it connected to; heartbeat is the reverse, and
today there's no way to answer "what is actually pinging this check" — useful
when a cron job's User-Agent changes after a migration, when a check gets
unexpected pings from an IP nobody recognizes, or just to confirm a new
script is wired up correctly.

This isn't a new problem class for the codebase: auth already solved the
identical need for login/session forensics. `user_tokens.properties`
stores a `created_with` blob (`{method, userAgent, remoteAddr}`, see
`server/internal/handlers/auth/service.go:171-183,337-344`) captured via
`extractRemoteAddress(req bunrouter.Request) string`
(`auth/handler.go:1096-1112`, X-Forwarded-For → X-Real-IP → `RemoteAddr`
fallback), and surfaced on the Sessions page
(`web/dash0/src/routes/orgs/$org/account.sessions.tsx`). Heartbeat should
follow the same pattern rather than invent a new one.

## Proposal

1. **Capture in the existing handler, no new endpoint.** In
   `heartbeat/handler.go`'s `ReceiveHeartbeat`, alongside the existing
   `org`/`identifier`/`token`/`status` extraction, read
   `req.Header.Get("User-Agent")`, the source IP, and `req.Method`
   (`bunrouter.Request` embeds `*http.Request`, so both are directly
   available — same as the existing `req.Header.Get("Content-Type")` at
   `handler.go:43`).

2. **Share the IP-extraction logic instead of writing a third copy.** The
   codebase already has two independent implementations of "extract client
   IP from XFF/X-Real-IP/RemoteAddr":
   `middleware/ratelimit.go:163-192` (`extractIP`, gated by a configurable
   `trustedProxies` count — it's security-relevant, since rate-limit bypass
   is the threat model) and `auth/handler.go:1096-1112`
   (`extractRemoteAddress`, unconditionally trusts XFF — fine for
   forensics/display, not used for any security decision). Heartbeat's need
   matches the auth one exactly (display-only forensics on a
   `bunrouter.Request`), so extract `extractRemoteAddress` into an exported
   helper on `server/internal/handlers/base` (e.g.
   `base.ExtractRemoteAddr(req bunrouter.Request) string`), update auth's
   call sites (`handler.go:107,369,562,974,1015`,
   `passkey_handler.go:140`) to use it, and have `heartbeat/handler.go` call
   the same helper. Leave `ratelimit.go`'s gated version alone — different
   security semantics, not the same concern.

3. **Store it on the per-ping `Output`, no DB migration.** `Output` is
   already a generic `models.JSONMap` returned as-is by the API
   (`results/service.go:317-318`, `if withSet["output"] { resp.Output = ... }`,
   whole-map passthrough, no per-key whitelist), and the OpenAPI schema
   already documents it as a generic `object`
   (`openapi.yaml:2118-2120,2493-2495`). Add three keys to the map built at
   `service.go:150`, matching the auth precedent's naming exactly —
   `userAgent`, `remoteAddr` — plus `httpMethod`:
   ```go
   Output: models.JSONMap{
       "message":    outputMessage,
       "userAgent":  userAgent,  // omit/blank if not sent
       "remoteAddr": remoteAddr,
       "httpMethod": req.Method, // "GET" or "POST"
   }
   ```
   No migration, no new columns on the shared `results` table (which every
   other check type also writes to — caller IP/UA is heartbeat-specific and
   has no business being a dedicated column there), no OpenAPI schema change.

4. **Surface it on the result detail page**, instead of leaving it buried in
   the raw JSON dump. `checks.$checkUid.results.$resultUid.tsx:246-257`
   currently renders the entire `output` map as
   `JSON.stringify(data.output, null, 2)`. Pull `userAgent`/`remoteAddr`/
   `httpMethod` out into their own small "Caller" card, styled like the
   existing Metrics card's label/value rows
   (`results.$resultUid.tsx:228-244`), and keep the remaining keys
   (`message`, plus whatever else lands in `output` in the future) in the
   existing raw-JSON card so nothing is duplicated.

## Out of scope

- Geo/ASN lookup, reverse DNS, or TLS fingerprinting of the caller — no such
  capture exists anywhere in the codebase today; not requested, adds an
  external dependency for speculative value.
- A dedicated "recent callers" table/view or caller-history UI beyond the
  per-ping result — every ping already produces its own `Result` row, which
  is the history; a rollup/summary view can be a follow-up if it turns out
  to be needed.
- Changing how `output` survives aggregation. The aggregation job carries
  the *last* raw row's `Output` in a bucket forward into the hourly/day/month
  rollup (`jobs/jobtypes/job_aggregation.go:758-761,980`,
  `state.lastOutput`), so one ping's caller info per bucket persists as long
  as the aggregate row does — but that's true of `output.message` for every
  check type today too. This spec doesn't change that behavior, just adds
  keys to a map that already flows through it.
- Consolidating `ratelimit.go`'s `extractIP` with the new shared helper —
  different security semantics (spoof-resistance for rate limiting vs.
  display-only forensics here); keep them separate.
- Worker-liveness heartbeats (`POST /api/v1/workers/heartbeat`,
  `handlers/workers/handler.go:64-93`) — an unrelated feature (workers
  reporting they're alive), not touched by this spec.

## Acceptance criteria

- Every heartbeat ping that reaches the service
  (`POST`/`GET /api/v1/heartbeat/:org/:identifier`) persists `userAgent`,
  `remoteAddr`, and `httpMethod` in that ping's `Result.output`, alongside
  the existing `message`.
- `remoteAddr` follows the X-Forwarded-For → X-Real-IP → `RemoteAddr`
  fallback order, via the shared, exported helper — not a third
  hand-rolled copy.
- `GET .../results?with=output` and the result-detail endpoint return the
  new keys with zero API/OpenAPI changes required.
- The result detail page shows a "Caller" card (User-Agent, Source IP,
  Method) whenever a result's `output` carries them; the raw Output JSON
  card no longer repeats those three keys.
- Legacy/pre-change heartbeat rows (no caller keys in `output`) render
  exactly as before — no crash, no empty "Caller" card.
- Auth's session/login forensics behavior is unchanged (same values, now
  produced by the shared helper).
- `make lint`, `make gotest`, and `make test-dash` all pass; new/updated
  unit tests cover `heartbeat.Service.ReceiveHeartbeat` persisting caller
  metadata and the extracted `base.ExtractRemoteAddr` helper.

## Implementation plan

- [ ] Extract `auth.extractRemoteAddress` into an exported
      `base.ExtractRemoteAddr(req bunrouter.Request) string` in
      `server/internal/handlers/base/base.go`; update its 6 call sites in
      `auth/handler.go` and `auth/passkey_handler.go` to use it.
- [ ] `heartbeat/handler.go`: capture `req.Header.Get("User-Agent")`,
      `base.ExtractRemoteAddr(req)`, and `req.Method`; pass all three into
      `svc.ReceiveHeartbeat(...)`.
- [ ] `heartbeat/service.go`: extend `ReceiveHeartbeat`'s signature with the
      three new params; add them to the `Output` map at `service.go:150`
      (blank/omit values that weren't sent, e.g. no `User-Agent` header).
- [ ] Frontend `checks.$checkUid.results.$resultUid.tsx`: add a "Caller"
      card (rendered when `data.output?.userAgent || data.output?.remoteAddr`)
      with labeled rows mirroring the Metrics card style; strip those keys
      out before `JSON.stringify`-ing the rest of `output`.
- [ ] Optional polish: note in `HeartbeatEndpoint`
      (`checks.$checkUid.index.tsx:142-186`) and in
      `web/docs/docs/features/check-types.md`'s Heartbeat section
      (around line 663) that caller metadata is now recorded, for user
      transparency.
- [ ] Unit tests: `heartbeat/service_test.go` (metadata persisted; sane
      defaults when `User-Agent` absent or request has no proxy headers),
      plus a test for `base.ExtractRemoteAddr` (can carry over the existing
      `auth` extraction test cases).
- [ ] `make lint`, `make gotest`, `make test-dash`.
