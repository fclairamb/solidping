# RDP checker — active Remote Desktop Protocol check type

## Context

solidping ships 37 protocol checkers behind a closed, switch-based registry
(`server/internal/checkers/registry/registry.go`); each is a self-contained
package under `server/internal/checkers/check<proto>/` implementing the
`checkerdef.Checker` interface (`checkerdef/interface.go:37-52`) and carrying a
`CheckTypeMeta` in the authoritative registry (`checkerdef/types.go:250-289`).
Adding one is a well-trodden path documented in
`server/internal/checkers/CLAUDE.md` ("Adding New Checkers", six steps) and
recently exercised by NTP (`specs/done/2026/06/2026-06-29-02-ntp-protocol-check.md`),
SIP, and Kubernetes.

What's missing is any **RDP** awareness. The closest thing today is the generic
TCP checker (`checktcp/`): a `tcp://host:3389` check proves the port is open,
but says nothing about whether the Remote Desktop service behind it actually
answers, or how it is configured. RDP hosts are typically reachable only from
inside a network — which is exactly where solidping's distributed workers run —
and their most common real-world failure mode is not "port closed" but "port
open, service broken or misconfigured" (certificate expired/replaced, NLA
policy flipped by GPO, TermService wedged after an update). The SSL checker's
`warningDays`/`criticalDays` → `warning`/`down` threshold shape (`checkssl`,
`StatusWarning`) already models the certificate half of this.

## The key questions

### Q1 — What does an RDP check actually *assert*?

The RDP connection sequence (MS-RDPBCGR §2.2.1.1/2.2.1.2) starts **pre-auth**
with an X.224 Connection Request carrying an RDP Negotiation Request
(`RDP_NEG_REQ`, the client's supported security protocols), answered by an
X.224 Connection Confirm carrying either `RDP_NEG_RSP` (the server's
`selectedProtocol`) or `RDP_NEG_FAILURE` (a failure code). This handshake needs
**no credentials** and gives three signals a bare TCP connect cannot:

- **Service liveness** — a valid Connection Confirm means the RDP listener
  (TermService, xrdp, …) parsed our request and answered, not just that a
  firewall forwards the port.
- **Security posture** — `selectedProtocol` says whether the server picked
  standard RDP security, TLS (`PROTOCOL_SSL`), NLA/CredSSP (`PROTOCOL_HYBRID`),
  `PROTOCOL_HYBRID_EX`, or `PROTOCOL_RDSTLS`. An optional `requireNLA` flag
  turns "NLA got silently disabled" into a detectable failure.
- **Certificate health** — when a TLS-based protocol is selected, the client's
  next step is a TLS handshake; completing it (without sending CredSSP) exposes
  the server certificate, so expiry can be checked SSL-checker-style. RDP certs
  are very often self-signed or from an internal CA, so the check inspects the
  leaf (`InsecureSkipVerify` + manual `NotAfter` read) rather than validating
  the chain.

**Decision:** v1 health = TCP connect + valid X.224 Connection Confirm.
`RDP_NEG_FAILURE` → `down` (failure code in output). On top, two optional
knobs: `requireNLA` (selected protocol must be HYBRID/HYBRID_EX, else `down`)
and SSL-style certificate `warningDays`/`criticalDays` (evaluated only when a
TLS-based protocol was negotiated).

### Q2 — Which library?

The opposite decision from NTP: **hand-roll it, no dependency**. There is no
small, maintained Go RDP client (`grdp` and friends are large, unmaintained
full-session clients). The pre-auth exchange is trivially small — the request
is 19 fixed bytes (TPKT header + X.224 CR TPDU + 8-byte `RDP_NEG_REQ`), the
response parse is a TPKT length + X.224 CC + optional 8-byte
`RDP_NEG_RSP`/`RDP_NEG_FAILURE` — and the TLS step is stdlib `crypto/tls`.
Constants (protocol bits `0x0/0x1/0x2/0x4/0x8`, failure codes `0x1`–`0x6`) come
straight from MS-RDPBCGR and get named Go consts with spec references.

**Decision:** ~100 lines of encode/parse in the `checkrdp` package, zero new
`go.mod` entries.

## Goal

A user can add an `rdp` check against a host `(host, [port=3389])` and get
`up`/`warning`/`down` from the pre-auth RDP negotiation — service liveness by
default, plus optional NLA enforcement and certificate-expiry thresholds — with
the negotiated security protocol, server negotiation flags, and certificate
details exposed in output/metrics. No credentials anywhere.

## Non-goals

- **Authentication / session establishment.** No CredSSP/NTLM/Kerberos, no
  login attempt, no screenshot. The check stops after the Connection Confirm
  (plus, when TLS-based, one TLS handshake) and closes cleanly.
- **RD Gateway** (RDP over HTTPS transport) and **RDP-over-UDP** (the 3389/UDP
  side channel) — v1 is the canonical TCP transport only.
- **Chain validation of the server certificate** — RDP certs are routinely
  self-signed; v1 reads expiry/subject/issuer only. A `verifyChain` option is a
  possible follow-up.
- **Load-balancer routing tokens** (the `Cookie: mstshash=` X.224 cookie) — not
  sent in v1; a config option if a user behind an RDS broker/LB needs it.
- **Replacing the generic TCP checker** for port probes — it stays the right
  tool when only reachability matters.

## Design

A greenfield checker modelled on `checktcp` (closest template: TCP,
`category:network`, no-credential) + the threshold/`warning` shape of
`checkssl`. Follows the six steps in `server/internal/checkers/CLAUDE.md`.

### Registration (the four wiring points)

1. `CheckTypeRDP CheckType = "rdp"` in the const block (`checkerdef/types.go:102-185`,
   alongside `CheckTypeTCP`).
2. A `CheckTypeMeta` in `checkTypesRegistry` (`types.go:250-289`) — **required**,
   or `activation_test.go` fails:
   `{Type: CheckTypeRDP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor RDP (Remote Desktop) servers", DefaultPeriod: <align with checktcp's>}`.
3. Add `CheckTypeRDP` to `ListCheckTypes()` (`types.go:341-382`).
4. Import + a `case` in **both** registry switches — `GetChecker`
   (`registry.go:68-149`) and `ParseConfig` (`registry.go:157-238`).

### `checkrdp` package

New `server/internal/checkers/checkrdp/{config.go,checker.go,protocol.go,samples.go}`
(no `secret_fields.go` — the handshake is pre-auth, no secrets).

- **`RDPConfig`** (`config.go`, `FromMap`/`GetConfig`/`Validate`, mirroring
  `checktcp/config.go:11-21` + numeric thresholds like `checkssl`):
  ```go
  type RDPConfig struct {
      Host         string        `json:"host,omitempty"`
      Port         int           `json:"port,omitempty"`          // default 3389
      Timeout      time.Duration `json:"timeout,omitempty"`       // default 5s, ≤60s
      RequireNLA   bool          `json:"require_nla,omitempty"`   // optional, off by default
      WarningDays  int           `json:"warning_days,omitempty"`  // optional cert threshold, 0 = off
      CriticalDays int           `json:"critical_days,omitempty"` // optional cert threshold, 0 = off
  }
  ```
  `Validate` (no network): `host` required; `port` defaults 3389, range
  1–65535; `timeout` `>0 && ≤60s`; thresholds `≥0` and
  `CriticalDays ≤ WarningDays` when both set (same ordering rule as
  `checkssl`); auto-name/slug from host as `checktcp` does.
- **`protocol.go`** — pure encode/parse, fully unit-testable without a socket:
  `buildConnectionRequest()` (TPKT + X.224 CR + `RDP_NEG_REQ` advertising
  `SSL|HYBRID|HYBRID_EX|RDSTLS`), `parseConnectionConfirm([]byte)` returning
  `{selectedProtocol, serverFlags}` or `{failureCode}` or "plain CC, no
  negotiation payload" (legacy standard-security servers answer a bare X.224 CC
  — still `up`, protocol reported as `rdp` standard security).
- **`RDPChecker.Execute`** (`checker.go`): dial TCP with the config timeout and
  `ctx`; write the request; read + parse the confirm; classify —
  - dial error / connection reset → `StatusDown`; context deadline →
    `StatusTimeout` (compare `ctx.Err()`, as `checktcp` does);
  - malformed/short/garbage response → `StatusDown` (something non-RDP answered);
  - `RDP_NEG_FAILURE` → `StatusDown` with the named failure code
    (`SSL_REQUIRED_BY_SERVER`, `HYBRID_REQUIRED_BY_SERVER`, …) in output;
  - `RequireNLA` set and selected ∉ {HYBRID, HYBRID_EX} → `StatusDown`;
  - TLS-based protocol selected → perform a `crypto/tls` handshake
    (`InsecureSkipVerify`, leaf inspected manually) on the same connection,
    read the leaf cert; cert already expired or ≤ `CriticalDays` →
    `StatusDown`; ≤ `WarningDays` → `StatusWarning`; TLS handshake itself
    failing → `StatusDown` (server offered TLS but can't complete it);
  - otherwise → `StatusUp`. Close the connection cleanly (FIN, no further PDUs).
- **Metrics** (`Result.Metrics`, unsuffixed names fall to type-based defaults,
  as `checkssl`'s `days_remaining` already does): `connect_ms`, `handshake_ms`,
  `days_remaining` (when a cert was read).
- **Output** (`Result.Output`, diagnostics): `host`, `port`,
  `selected_protocol` (`rdp` | `tls` | `nla` | `nla_ex` | `rdstls`),
  `server_flags` (e.g. `RESTRICTED_ADMIN_MODE_SUPPORTED`), and when a cert was
  read `cert_subject`, `cert_issuer`, `cert_expires_at` (RFC3339),
  `cert_self_signed` (bool); on failure `error` (+ `failure_code` when the
  server sent `RDP_NEG_FAILURE`), reusing `checkerdef.OutputKeyError`/`OutputKeyHost`/`OutputKeyPort`.
- **`samples.go`** (`CheckerSamplesProvider`): no public RDP endpoints exist, so
  a placeholder-host sample (e.g. `rdp.example.internal:3389`, `requireNLA`
  on), following whatever pattern the other internal-only checkers
  (`checkmssql`, `checkdocker`, `checkkubernetes`) use — verify at
  implementation.

### Frontend (`web/dash0/`)

`check-form.tsx` is a hand-maintained monolith; a check type is wired in
several keyed places (use the **tcp** case as the copy source; line refs
current as of writing, expect drift):
- `CheckType` union (`check-form.tsx:56`) + the `checkTypes` options array
  (`:69`): `{ value: "rdp", label: "RDP", description: "Monitor RDP (Remote Desktop) servers" }`.
- Per-type state + a `case "rdp":` in each keyed switch: the state→config
  builder (`buildConfig`, `:668` region), the config-map effect, and the
  render-fields switch. Reuse the host/port/timeout inputs; add a
  `requireNLA` switch and the SSL-style warning/critical-days numeric inputs.
- `web/dash0/src/api/hooks.ts` — add `"rdp"` to the two check-`type` unions
  (`hooks.ts:46`, `:101`).
- i18n: add `rdp: "RDP"` under the `checkTypes` object in
  `web/dash0/src/locales/{en,fr,de,es}/checks.json`.
- Per `web/dash0/CLAUDE.md`, build from the design-reference primitives the
  existing cases already use; no raw Radix.

### Docs / API

- `web/docs/docs/features/check-types.md` — new `### RDP` under "## Network
  Checks" with an options table (Host, Port=3389, Timeout, Require NLA,
  Warning/Critical days) and a short note that workers must have network
  access to the RDP host (typically internal); bump the check-type count.
- `web/docs/docs/intro.md` — bump the "Check Types" count (`intro.md:13`,
  `:70`; currently says 36 — reconcile with the actual registry count while
  there) and add RDP to the list.
- OpenAPI: check `type` on the create path is a free-form string, so no enum
  change expected — verify, then `make generate`.

## Files to create / modify

**New (backend):** `server/internal/checkers/checkrdp/{config.go,checker.go,protocol.go,samples.go}` + `checker_test.go`, `protocol_test.go`.

**Modified (backend):**
- `server/internal/checkers/checkerdef/types.go` — `CheckTypeRDP` const, `CheckTypeMeta`, `ListCheckTypes`.
- `server/internal/checkers/registry/registry.go` — import + `case` in both switches.
- No `go.mod` change (stdlib only).

**Modified (frontend):** `web/dash0/src/components/shared/check-form.tsx`, `web/dash0/src/api/hooks.ts`, `web/dash0/src/locales/{en,fr,de,es}/checks.json`.

**Modified (docs):** `web/docs/docs/features/check-types.md`, `web/docs/docs/intro.md`; OpenAPI verify.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` — `server/CLAUDE.md`):**
  - `RDPConfig.Validate`: missing host → error; port default 3389 +
    out-of-range error; timeout bounds; threshold ordering; name/slug auto-fill.
  - `protocol.go`: golden-byte test for `buildConnectionRequest()`;
    `parseConnectionConfirm` over canned byte tables — NEG_RSP for each
    protocol, every NEG_FAILURE code, plain CC without payload, truncated TPKT,
    wrong TPKT version, garbage.
  - `RDPChecker.Execute` against an **in-process fake RDP server**
    (`net.Listen` on `127.0.0.1:0`, canned responses per test case — no
    injectable func var needed, the real dial path is exercised): NEG_RSP(HYBRID)
    → up; NEG_RSP(SSL) with `requireNLA` → down; NEG_FAILURE → down with
    failure code; plain CC → up (standard security); TLS-based case with a
    `tls.Listener` + self-signed cert near expiry → warning/down per
    thresholds; connection refused → down; unresponsive listener → timeout.
- **End-to-end** (`make dev-test`): add an `rdp` check from the dash0 form
  against a real Windows host or an `xrdp` container
  (`docker run -p 3389:3389 …`), confirm `up` with `selected_protocol` and cert
  fields rendered; enable `requireNLA` against a non-NLA server → `down`;
  point at a closed port → `down`.
- **Guards:** a non-RDP service on 3389 (e.g. an HTTP server) → `down`, not a
  crash; response larger than expected → parse rejects, no unbounded read
  (cap the TPKT read at its declared length, itself capped).
- `make build && make lint && make test` (backend); dash0 lint + Playwright.

## Risk log

| Risk | Mitigation |
|---|---|
| Repeated pre-auth probes show up in Windows event logs / IDS as connection noise | Handshake stops pre-auth and closes cleanly (no auth failure events are generated, only connection events); default period is modest; documented in `check-types.md` |
| Hand-rolled protocol bytes drift from spec | Exchange is 19 fixed bytes + a bounded parse; golden-byte unit tests; constants carry MS-RDPBCGR section references; legacy plain-CC servers explicitly handled |
| RDP certs are self-signed → naive TLS validation would false-alarm | Leaf-only inspection (`InsecureSkipVerify` + manual expiry read), chain validation an explicit non-goal; `cert_self_signed` surfaced in output |
| RDS brokers / load balancers may expect an X.224 routing cookie and reset otherwise | Surfaces deterministically as `down`; documented; `mstshash` cookie option listed as follow-up |
| New first-class check type must be registered in every keyed location or it half-works | The six steps in `checkers/CLAUDE.md` are followed; `activation_test.go` pins that every `CheckTypeMeta` is wired; both registry switches updated |
| No public RDP endpoint for samples or CI E2E | Samples use a placeholder host like other internal-only checkers; unit tests run a fake in-process server, so CI needs no real RDP host |

**Status**: Todo | **Created**: 2026-07-10

## Implementation Plan

One vertical slice (no DB migration — `checks.type` is a free-form string
column and `checks.config` is jsonb).

1. **Types:** `checkerdef/types.go` — `CheckTypeRDP` const, `CheckTypeMeta`
   (`safe`/`standalone`/`category:network`), `ListCheckTypes`.
2. **Package `checkrdp`:** `protocol.go` (encode/parse + MS-RDPBCGR consts),
   `config.go` (struct + `FromMap`/`GetConfig`/`Validate`), `checker.go`
   (`Type`/`Validate`/`Execute`), `samples.go`.
3. **Registry:** `registry.go` — import + `case` in `GetChecker` and `ParseConfig`.
4. **Tests:** `protocol_test.go` golden bytes; `checker_test.go` fake-server
   tables per Verification.
5. **Frontend:** `check-form.tsx` (union, options, state, the keyed `case
   "rdp":` switches), `hooks.ts` unions, `checks.json` label in en/fr/de/es.
6. **Docs:** `check-types.md` section + count, `intro.md` count/list; OpenAPI
   verify; `make generate`.
7. `make build && make lint && make test`; dash0 lint + Playwright.
