# NTP checker — active Network Time Protocol check type

## Context

solidping ships ~35 protocol checkers behind a closed, switch-based registry
(`server/internal/checkers/registry/registry.go`); each is a self-contained
package under `server/internal/checkers/check<proto>/` implementing the
`checkerdef.Checker` interface (`checkerdef/interface.go:37-52`) and carrying a
`CheckTypeMeta` in the authoritative registry (`checkerdef/types.go:234-271`).
Adding one is a well-trodden path documented in
`server/internal/checkers/CLAUDE.md` ("Adding New Checkers", six steps).

What's missing is any **time-source** awareness. The closest thing today is a
**sample** on the generic UDP checker — `"NTP Pool (123)"`
(`checkudp/samples.go:26-35`) — but that only proves *UDP/123 is reachable*; it
sends no NTP packet and learns nothing about the server as a clock. A `freebox_line`
gauge checker and the SSL/Domain "days-remaining" checkers already show the
pattern for a checker that reports a measured value plus warn/critical thresholds
and can land on `warning` as well as `up`/`down` (`checkerdef/types.go:8-29`,
`StatusWarning = 8`). NTP is the natural next network checker, and it's one of the
simplest: a single UDP request/response, no connection, no credentials.

## The key questions

### Q1 — What does an NTP check actually *assert*?

A plain "UDP/123 answered" check already exists (the UDP sample). The value of a
dedicated NTP checker is reading the **NTP response** and judging the server *as a
time source*:

- **Reachability** — did the server return a valid NTP response at all.
- **Server self-health** — the response's own fields say whether the server is a
  usable clock: `Stratum` (1–15 good; **0** = "Kiss-o'-Death", **16** =
  unsynchronized), `Leap` indicator (`LeapNotInSync` = the server itself lost
  sync), and root distance. `github.com/beevik/ntp` packages exactly this as
  `(*Response).Validate()`, which returns an error when the response indicates an
  unhealthy/unusable server. This is the headline health signal and needs **no
  trust in the worker's own clock**.
- **Clock offset** (optional, opt-in) — how far the server's clock is from the
  worker's (`Response.ClockOffset`). Useful, but **measured relative to the worker
  clock**, so it's an *optional* warn/critical threshold (SSL-style), not the
  default pass/fail — with a clear doc caveat (see Risk log).
- **Response time** — `Response.RTT`, used as the check's `Duration` like every
  other checker's latency.

**Decision:** v1 health = *reachable* **and** `Validate()` passes. On top, two
**optional** SSL-style thresholds the user opts into: an **offset** warn/critical
(ms) and a **max stratum**. This mirrors `checkssl`'s `warningDays`/`criticalDays`
→ `warning`/`down` and reuses the existing `StatusWarning`.

### Q2 — Which library?

`github.com/beevik/ntp` is the de-facto Go SNTP client: one call,
`ntp.QueryWithOptions(host, ntp.QueryOptions{Timeout, Port, Version})` →
`*ntp.Response` exposing `ClockOffset`, `RTT`, `Stratum`, `Precision`,
`RootDelay`, `RootDispersion`, `RootDistance`, `Leap`, `ReferenceID`, `Poll`,
`KissCode`, plus `Validate()`. Tiny dependency tree (nothing like the k8s SDK
pulled in by `2026-06-21-02-kubernetes-checker.md`). No reasonable case for
hand-rolling the 48-byte packet.

**Decision:** add `github.com/beevik/ntp` to `server/go.mod`.

## Goal

A user can add an `ntp` check against a time server `(host, [port=123])` and get
`up`/`warning`/`down` from the server's NTP response — reachability + the server's
self-reported health by default, plus optional clock-offset and stratum
thresholds — with offset, RTT, stratum, and root delay/dispersion exposed as
metrics. Default period suited to time servers (5 min). No secrets anywhere.

## Non-goals

- **Disciplining or setting any clock.** Strictly read-only measurement.
- **NTP authentication** (symmetric keys, NTS/Network Time Security) — v1 is
  plain SNTP. A possible follow-up.
- **NTPv5, mode-6/7 control queries, `ntpq`-style peer/association listings** —
  v1 is a single client query (mode 3 → mode 4 response).
- **Replacing the generic UDP "NTP Pool" sample** — left as-is (it demonstrates
  raw UDP). The new checker's samples are the recommended way to monitor time.
- **Trusting the worker clock as ground truth.** Offset is an opt-in threshold,
  not the default verdict (Q1).

## Design

A greenfield checker modelled on `checkudp` (closest template: a UDP,
`category:network`, no-credential checker) + the threshold/`warning` shape of
`checkssl`. Follows the six steps in `server/internal/checkers/CLAUDE.md`.

### Registration (the four wiring points)

1. `CheckTypeNTP CheckType = "ntp"` in the const block (`checkerdef/types.go:102-176`,
   alongside `CheckTypeUDP`).
2. A `CheckTypeMeta` in `checkTypesRegistry` (`types.go:234-271`) — **required**, or
   `activation_test.go:19` (`r.Len(enabled, len(ListCheckTypeMetas()))`) fails:
   `{Type: CheckTypeNTP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor NTP time servers", DefaultPeriod: 5 * time.Minute}`.
3. Add `CheckTypeNTP` to `ListCheckTypes()` (`types.go:323-362`).
4. Import + a `case` in **both** registry switches — `GetChecker`
   (`registry.go:66-143`) and `ParseConfig` (`registry.go:151-228`).

### `checkntp` package

New `server/internal/checkers/checkntp/{config.go,checker.go,samples.go}` (no
`secret_fields.go` — NTP carries no secrets, unlike `checksmtp`).

- **`NTPConfig`** (`config.go`, `FromMap`/`GetConfig`/`Validate`, mirroring
  `checkudp/config.go` + numeric thresholds like `checkssl`):
  ```go
  type NTPConfig struct {
      Host           string        `json:"host,omitempty"`
      Port           int           `json:"port,omitempty"`            // default 123
      Timeout        time.Duration `json:"timeout,omitempty"`         // default 5s, ≤60s
      Version        int           `json:"version,omitempty"`         // default 4, ∈ {3,4}
      OffsetWarnMs   int           `json:"offset_warn_ms,omitempty"`  // optional, 0 = off
      OffsetCritMs   int           `json:"offset_crit_ms,omitempty"`  // optional, 0 = off
      MaxStratum     int           `json:"max_stratum,omitempty"`     // optional, 0 = off (defaults to ≤15 via Validate())
  }
  ```
  `Validate` (no network): `host` required; `port` defaults 123, range 1–65535;
  `timeout` `>0 && ≤60s`; `version ∈ {3,4}`; thresholds `≥0` and
  `OffsetWarnMs ≤ OffsetCritMs` when both set; auto-name/slug from host (as
  `checkudp/checker.go:55-62`).
- **`NTPChecker.Execute`** (`checker.go`): `ntp.QueryWithOptions(cfg.Host,
  ntp.QueryOptions{Port, Timeout, Version})`; then classify —
  - transport error / no response → `StatusDown`; context-deadline → `StatusTimeout`
    (compare `ctx.Err()`, as `checkudp/checker.go:159-171`);
  - `resp.Validate()` fails (stratum 0/16, `LeapNotInSync`, bad root distance, Kiss
    code) → `StatusDown` (`error` + `kiss_code` in output);
  - `OffsetCritMs`/`MaxStratum` exceeded → `StatusDown`; `OffsetWarnMs` exceeded
    (within crit) → `StatusWarning`;
  - otherwise → `StatusUp`. Set `Result.Duration = resp.RTT`.
- **Metrics** (`Result.Metrics`, aggregatable — see suffix convention in
  `server/internal/jobs/jobtypes/job_aggregation.go:402-428`; unsuffixed names fall
  to type-based defaults, as `checkssl`'s `days_remaining` and `checkudp`'s
  `bytes_sent` already do): `offset_ms` (signed), `rtt_ms`, `stratum`,
  `root_delay_ms`, `root_dispersion_ms`, `root_distance_ms`, `poll_s`,
  `precision_ms`.
- **Output** (`Result.Output`, diagnostics): `host`, `port`, `stratum`,
  `leap` (string), `reference_id`, `server_time` (RFC3339), `offset` (human, e.g.
  `+12ms`); on failure `error` (+ `kiss_code` when present), reusing
  `checkerdef.OutputKeyError`/`OutputKeyHost`/`OutputKeyPort`.
- **`samples.go`** (`CheckerSamplesProvider`): `pool.ntp.org`, `time.google.com`,
  `time.cloudflare.com`, period 5 min — picked up automatically by
  `registry.GetAllSampleConfigs`.

### Frontend (`web/dash0/`)

`check-form.tsx` is a hand-maintained monolith (no codegen header); a check type
is wired in several keyed places (use the **udp** case as the copy source, line
refs current as of writing, expect drift):
- `CheckType` union (`check-form.tsx:60-65` region) + the `checkTypes` options
  array (`:66-101`): `{ value: "ntp", label: "NTP", description: "Monitor NTP time servers" }`.
- Per-type `useState` for ntp fields + a `case "ntp":` in each keyed switch: the
  state→config builder (udp at `:658`), the config-map effect (udp at `:878`), and
  the render-fields switch (udp at `:1299`). Reuse the existing host/port/timeout
  inputs; add numeric inputs for offset warn/critical (ms) and max stratum (the
  SSL warning/critical-days field shape) and a version select (3/4).
- `web/dash0/src/api/hooks.ts` — add `"ntp"` to the two check-`type` unions.
- i18n check-type label map: add `ntp: "NTP"` to
  `web/dash0/src/locales/{en,fr,de,es}/checks.json` (the `checkTypes` object that
  already holds `smtp`/`sip`/…).
- Per `web/dash0/CLAUDE.md`, build from the design-reference primitives the
  existing cases already use (`Input`, `Label`, `Select`); no raw Radix.

### Docs / API

- `web/docs/docs/features/check-types.md` — new `### NTP` under "## Network
  Checks" with an options table (Host, Port=123, Timeout, Version, Offset warn/crit
  ms, Max stratum); bump the "35 check types" count.
- `web/docs/docs/intro.md` — bump the "35 Check Types" line and add NTP to the list.
- OpenAPI: the check `type` in `CreateCheckRequest`/`UpsertCheckRequest`
  (`server/internal/app/openapi/openapi.yaml:1869,1935`) is a free-form `string`
  (the 6-value `enum`s at `:1799/1885/1947` are narrower summary schemas, not the
  create path — the existing 35 types already aren't enumerated there), so **no
  enum change is expected**; verify and, if any example/enum does list types, add
  `ntp`. Run `make generate` afterward (regenerates the Go client).

## Files to create / modify

**New (backend):** `server/internal/checkers/checkntp/{config.go,checker.go,samples.go}` + `checker_test.go`.

**Modified (backend):**
- `server/go.mod` / `go.sum` — add `github.com/beevik/ntp` (pinned; `go mod tidy` reviewed).
- `server/internal/checkers/checkerdef/types.go` — `CheckTypeNTP` const, `CheckTypeMeta`, `ListCheckTypes`.
- `server/internal/checkers/registry/registry.go` — import + `case` in both switches.

**Modified (frontend):** `web/dash0/src/components/shared/check-form.tsx`, `web/dash0/src/api/hooks.ts`, `web/dash0/src/locales/{en,fr,de,es}/checks.json`.

**Modified (docs):** `web/docs/docs/features/check-types.md`, `web/docs/docs/intro.md`; OpenAPI verify.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` — `server/CLAUDE.md`):**
  - `NTPConfig.Validate`: missing host → error; port default 123 + out-of-range
    error; timeout bounds; version ∈ {3,4}; threshold ordering
    (`warn > crit` → error); name/slug auto-fill.
  - `NTPChecker.Execute` with the `ntp.QueryWithOptions` call behind an injectable
    package-level func var (mirror the resolver-indirection used by
    `checkfreeboxline`/`checkkubernetes`) so tests feed synthetic `*ntp.Response`
    + errors without a network: healthy stratum-2 → up; stratum 16 / `LeapNotInSync`
    → down; offset over warn (within crit) → warning; offset over crit → down;
    stratum over `MaxStratum` → down; transport error → down; deadline → timeout.
  - Metrics/output keys present and correctly typed.
- **End-to-end** (`make dev-test`): add an `ntp` check for `pool.ntp.org` from the
  dash0 form, confirm it reports `up` with offset/RTT/stratum metrics rendered;
  set a deliberately tiny `offset_crit_ms` → check goes `down`; point at an
  unreachable host → `down`/`timeout`.
- **Guards:** worker without outbound UDP/123 → `down`/`timeout`, no panic; a
  malformed/short response → `down`, not a crash.
- `make build && make lint && make test` (backend); `make lint` + Playwright for dash0.

## Risk log

| Risk | Mitigation |
|---|---|
| Clock **offset is relative to the worker's own clock** — a skewed worker yields false offset alerts, worse across a distributed multi-worker fleet | Default verdict is reachability + `resp.Validate()` (server self-health), which needs no trust in the worker clock; offset is an **opt-in** warn/critical threshold, documented as worker-relative in `check-types.md` |
| Outbound **UDP/123 is frequently egress-firewalled** on worker hosts | Surfaces deterministically as `down`/`timeout`; documented as a prerequisite, same as ICMP raw-socket / Docker-socket caveats for those checkers |
| New first-class check type must be registered in every keyed location or it half-works | The six steps in `checkers/CLAUDE.md` are followed; `activation_test.go` pins that every `CheckTypeMeta` is wired; both registry switches updated |
| Redundancy with the existing UDP "NTP Pool" reachability sample | Different checker, different value (real NTP semantics vs. UDP/123 open); UDP sample left untouched, docs steer users to the NTP checker |
| `beevik/ntp` is a third-party dep | Tiny, widely used, single-purpose; pinned + `go mod tidy` reviewed; far lighter than other vendored checker deps (Docker SDK, k8s client-go) |

**Status**: Todo | **Created**: 2026-06-29

## Implementation Plan

One vertical slice (no DB migration — `checks.type` is a free-form string column
and `checks.config` is jsonb; a new `CheckType` value needs no schema change).

1. **Dependency:** `server/go.mod`/`go.sum` add `github.com/beevik/ntp`; `go mod tidy`.
2. **Types:** `checkerdef/types.go` — `CheckTypeNTP` const, `CheckTypeMeta`
   (`safe`/`standalone`/`category:network`, `DefaultPeriod: 5m`), `ListCheckTypes`.
3. **Package `checkntp`:** `config.go` (struct + `FromMap`/`GetConfig`/`Validate`),
   `checker.go` (`Type`/`Validate`/`Execute` with the injectable query func),
   `samples.go` (pool/google/cloudflare).
4. **Registry:** `registry.go` — import + `case` in `GetChecker` and `ParseConfig`.
5. **Tests:** `checkntp/checker_test.go` — config + execute tables per Verification.
6. **Frontend:** `check-form.tsx` (union, options, state, three `case "ntp":`),
   `hooks.ts` unions, `checks.json` label in en/fr/de/es.
7. **Docs:** `check-types.md` section + count, `intro.md` count/list; OpenAPI verify;
   `make generate`.
8. `make build && make lint && make test`; dash0 lint + Playwright.
