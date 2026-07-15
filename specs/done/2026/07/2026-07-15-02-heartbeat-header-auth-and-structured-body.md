---
model: sonnet
effort: medium
---

# Heartbeat ingestion: accept a header-borne token and a structured body

## Problem

The heartbeat endpoint (`POST|GET /api/v1/heartbeat/:org/:identifier`) is the
one place where an external caller pushes to SolidPing rather than the reverse.
Two aspects of its request shape are weaker than they need to be, and both
become visible as soon as a first-class CI integration starts using it (see the
companion spec `2026-07-15-03-solidping-action-github-actions-heartbeat.md`).

### 1. The token is only readable from the query string

`handler.go:38` reads the check token exclusively from the URL:

```go
token := req.URL.Query().Get("token")
```

A secret in a query string is a known weak spot: it lands in reverse-proxy and
CDN access logs, in `Referer` headers, in browser history when someone pastes
the URL, and in any intermediary that records URLs. SolidPing itself does the
right thing everywhere else — the API uses `Authorization: Bearer`. Heartbeat
is the exception, purely for the convenience of `curl <url>` with no flags.

The query form must keep working forever (every existing user's cron job
depends on it), but there is no reason a caller that *can* set a header should
be forced to put its secret in a URL.

### 2. The body carries exactly one string

`heartbeatBody` (`handler.go:29-32`) is:

```go
type heartbeatBody struct {
    Message string `json:"message"`
}
```

Any caller with structured context to report — a CI run URL, a commit SHA, a
record count, a batch ID — has to flatten it into one prose string, which is
then unparseable on the way out. Meanwhile `Result.Output` is already a generic
`models.JSONMap` returned whole-map by the results API with no per-key
whitelist (`results/service.go:317-318`), and the OpenAPI schema already
documents it as a generic `object` (`openapi.yaml:2118-2120,2493-2495`). The
plumbing for structured per-ping data exists end to end; only the ingestion
struct throws it away.

Spec `2026-07-05-12-heartbeat-ping-caller-metadata` already established the
pattern of adding keys to that `Output` map (`userAgent`, `remoteAddr`,
`httpMethod`) with no migration and no schema change. This spec extends the
same seam to caller-supplied data.

## Proposal

### 1. Header-borne token, query fallback

In `ReceiveHeartbeat` (`handler.go:35`), resolve the token as:

1. `Authorization: Bearer <token>` if present,
2. else `?token=` (unchanged behaviour),
3. else `ErrMissingToken` → 401 (unchanged).

Header wins when both are supplied. This is purely additive: no existing caller
changes, and `handler.go`'s existing `ErrMissingToken` / `ErrInvalidToken`
mapping (`handler.go:74-77`) is untouched. Keep the extraction in a small
helper in the heartbeat handler — this is a bespoke per-check token, not a JWT,
so it must **not** go anywhere near the `RequireAuth` middleware chain.

### 2. Structured body, namespaced under `output.data`

Replace the fixed `heartbeatBody` struct with a `map[string]any` decode. Pull
`message` out as today (preserving its exact current semantics), and carry the
**remaining** keys through to the service as a metadata map.

Store them **nested under a `data` key**, not flattened into `Output`:

```go
Output: models.JSONMap{
    "message":    outputMessage,
    "userAgent":  userAgent,
    "remoteAddr": remoteAddr,
    "httpMethod": httpMethod,
    "data":       callerData, // omitted entirely when empty
}
```

Nesting is deliberate. Flattening would let a caller overwrite the
server-captured forensics keys (`remoteAddr`, `userAgent`, `httpMethod`) simply
by including them in its JSON body — a caller must never be able to forge the
fields we record *about* it. Nesting removes that entire collision class and
keeps provenance obvious: everything under `data` is caller-asserted, everything
beside it is server-observed.

### 3. Bound the body

`Output` is a JSONB column on the shared `results` table, written on every
ping of an unauthenticated-until-token-checked endpoint. Wrap the body in
`http.MaxBytesReader` with a small cap (**8 KiB** — generous for a CI payload,
far below anything that hurts) before decoding. Over-cap bodies are rejected
with 400 / `ErrorCodeValidationError` rather than silently truncated.

Note the existing decode is deliberately lenient — `if err := ...Decode(&body);
err == nil` (`handler.go:45`) swallows malformed JSON and proceeds with an
empty message. **Preserve that leniency for parse errors** (a broken body must
never fail a liveness ping), but the size cap is a hard rejection: it is the
one case where we must not accept the write.

### 4. Surface `data` on the result detail page

`checks.$checkUid.results.$resultUid.tsx` already renders a "Caller" card and a
raw-JSON card for the rest of `output`. Render `output.data` in its own card
(label/value rows for scalar values, raw JSON for nested ones), and keep it out
of the leftover raw-JSON dump so nothing is shown twice.

## Out of scope

- Reworking the token itself (rotation, expiry, per-caller tokens). The token
  stays the auto-generated per-check hex string from
  `checkheartbeat/checker.go:34-41`.
- Applying header auth to any other endpoint, or touching the `RequireAuth` /
  `ServiceTokenBypass` middleware chain.
- Indexing or querying on `output.data` — it is display/forensics data, same as
  the caller metadata beside it.
- Changing aggregation behaviour. As documented in
  `2026-07-05-12-heartbeat-ping-caller-metadata`, the rollup carries the last
  raw row's `Output` forward (`job_aggregation.go:758-761,980`); `data` inherits
  that behaviour unchanged.
- Rate limiting the heartbeat endpoint (tracked separately in
  `specs/backlog/2026-03-30-org-check-rate-limit.md`).

## Acceptance criteria

- `Authorization: Bearer <token>` authenticates a heartbeat ping identically to
  `?token=`; header takes precedence when both are present; neither present
  still yields 401 `ErrMissingToken`.
- Every existing query-string caller behaves **exactly** as before — no
  deprecation, no warning, no behaviour change.
- A JSON body's non-`message` keys are persisted under `Result.output.data`,
  and `message` continues to land at `Result.output.message`.
- A body attempting to set `remoteAddr` / `userAgent` / `httpMethod` cannot
  overwrite the server-captured values; those keys land inside `data`.
- Bodies over 8 KiB are rejected with 400; malformed JSON under the cap is still
  tolerated (empty message, ping still recorded), as today.
- Pings with no body, or a body with only `message`, produce **no** `data` key
  at all (not an empty object).
- `GET .../results?with=output` and the result-detail endpoint return `data`
  with zero OpenAPI changes.
- The result detail page shows a "Data" card when `output.data` is present; the
  raw Output JSON card does not repeat those keys; legacy rows without `data`
  render exactly as before.
- `make lint`, `make gotest`, and `make test-dash` pass.

## Implementation plan

- [ ] `heartbeat/handler.go`: extract the token via a helper that prefers
      `Authorization: Bearer` and falls back to `?token=`.
- [ ] `heartbeat/handler.go`: wrap the body in `http.MaxBytesReader` (8 KiB);
      decode into `map[string]any`; split `message` from the rest; return 400 on
      over-cap, stay lenient on parse errors.
- [ ] `heartbeat/service.go`: extend `ReceiveHeartbeat` with a
      `callerData map[string]any` param; add it to the `Output` map under
      `"data"`, omitting the key entirely when the map is empty.
- [ ] Frontend `checks.$checkUid.results.$resultUid.tsx`: add a "Data" card
      rendered when `output.data` is non-empty; strip `data` from the raw-JSON
      card's payload.
- [ ] Docs: update the Heartbeat section of
      `web/docs/docs/features/check-types.md` (~line 663) to document both the
      `Authorization: Bearer` option and the structured body, with a curl
      example of each.
- [ ] Tests: `heartbeat/handler_test.go` — header auth, query auth, header
      precedence, missing token, over-cap body rejected, malformed body
      tolerated. `heartbeat/service_test.go` — `data` persisted, `data` omitted
      when empty, caller cannot forge `remoteAddr`/`userAgent`/`httpMethod`.
- [ ] `make lint`, `make gotest`, `make test-dash`.

## Implementation Plan

1. `heartbeat/handler.go`:
   - Add `extractToken(req)` helper: checks `Authorization: Bearer <token>` header
     first (via `req.Header.Get("Authorization")`, `strings.CutPrefix` on
     `"Bearer "`), falls back to `req.URL.Query().Get("token")`.
   - Wrap `req.Body` in `http.MaxBytesReader(writer, req.Body, 8*1024)` before
     decoding. Decode into `map[string]any`. On a `*http.MaxBytesError` (Go
     1.19+ typed error from the reader), return a new `ErrBodyTooLarge`
     sentinel mapped to 400 `ErrorCodeValidationError`. On any other decode
     error, swallow it exactly as today (lenient — empty message, no data).
   - Pull `message` out of the decoded map (type-assert to string; missing or
     non-string key -> empty message, matching today's zero-value behavior).
     Delete the `message` key from the map, pass the remainder as
     `callerData map[string]any` to the service (nil/empty map when nothing
     left).
2. `heartbeat/service.go`:
   - `ReceiveHeartbeat` gains a `callerData map[string]any` parameter.
   - `buildHeartbeatOutput` gains a `callerData` param; sets `output["data"] =
     callerData` only when `len(callerData) > 0` — never an empty map, and
     never merged/flattened into the top-level map, so caller-supplied
     `remoteAddr`/`userAgent`/`httpMethod` keys stay confined inside `data`
     and cannot shadow the server-observed siblings.
3. `heartbeat/handler_test.go` (new file): header auth accepted, query auth
   accepted (unchanged), header wins when both present, missing both -> 401
   `ErrMissingToken`, >8KiB body -> 400 validation error, malformed JSON under
   cap -> 200 with empty message.
4. `heartbeat/service_test.go`: extend existing calls with the new
   `callerData` param (nil for existing tests); add cases for `data`
   persisted, `data` omitted when empty/only-message, and a body supplying
   `remoteAddr`/`userAgent`/`httpMethod` keys landing inside `data` without
   overwriting the sibling server-captured values.
5. Frontend `checks.$checkUid.results.$resultUid.tsx`: destructure `data` out
   of `output` alongside `userAgent`/`remoteAddr`/`httpMethod` before building
   `rawDump` (so it isn't double-shown); add a "Data" card (new
   `checks:resultDetail.data` label) rendered when `output.data` is a
   non-empty object — scalar entries as label/value rows (mirroring the
   Metrics card), non-scalar (object/array) entries as an inline
   `JSON.stringify` block, same visual language as the existing raw-JSON
   card.
6. Docs `web/docs/docs/features/check-types.md` Heartbeat section: add the
   `Authorization: Bearer` curl example and a structured-body POST curl
   example (`-H "Authorization: Bearer <token>" -d '{"message":"...","runId":"..."}'`).
7. `make fmt` before each commit; run `make build-backend lint-back test`,
   `make build-dash0`, `bun run lint` (dash0) until green.
