# SSL check — graduated expiry thresholds + whole-chain expiry & reporting

## Context

Inspired by the Maintenant competitor analysis ([`docs/competitors/maintenant.md`](../../docs/competitors/maintenant.md)),
whose SSL monitoring fires **graduated** expiry alerts (30 / 14 / 7 / 3 / 1 day) and
advertises full certificate-chain validation. SolidPing's SSL check is thinner today:

- A **single** `ThresholdDays` (default 30) — [`checkssl/config.go:28`](../../server/internal/checkers/checkssl/config.go).
- `buildResult` flips the status to **`StatusDown` when `daysRemaining <= threshold`**, and
  `daysRemaining` is computed **only from the leaf cert** (`PeerCertificates[0]`) —
  [`checkssl/checker.go:235-241`](../../server/internal/checkers/checkssl/checker.go).

Two facts that change the framing:

1. **Chain *trust* is already validated.** The check dials with
   `tls.Client(conn, &tls.Config{ServerName: serverName})` and **no `InsecureSkipVerify`**
   ([`checkssl/checker.go:95-99`](../../server/internal/checkers/checkssl/checker.go)), so
   `HandshakeContext` performs full RFC-5280 verification against the system root store
   (chain building, intermediate trust, hostname match, per-cert validity). An expired,
   untrusted, or incomplete chain **already fails the handshake** → `handleConnectError`
   → `StatusDown`. So "chain validation" in the trust sense is not missing. What *is*
   missing is (a) graduated **warning** before expiry, (b) considering the **whole chain's
   nearest expiry** (not just the leaf), and (c) **reporting** the chain to the dashboard.

2. **There is no "warning" / "degraded" status.** The status enum is
   `Up(3) / Down(4) / Timeout(5) / Error(6)` (+ Created/Running) —
   [`checkerdef/types.go:13-17`](../../server/internal/checkers/checkerdef/types.go).
   This is the single biggest constraint on the design.

Note: [`checkdomain`](../../server/internal/checkers/checkdomain/checker.go) (WHOIS domain
expiry) has the **identical** single-threshold-binary pattern
([`checkdomain/checker.go:96-98`](../../server/internal/checkers/checkdomain/checker.go)).
The graduated-threshold logic should be written so it can be shared with `checkdomain`.

## My honest opinion

You asked for "graduated thresholds + chain validation." Here's what I'd actually build and why.

**1. The current "Down at 30 days" behaviour is a misfeature.** A perfectly valid certificate
that simply expires in 29 days is reported as a **month-long outage** — it pollutes uptime %,
spawns an incident, and pages people for something that isn't an outage. The right model is:
expiry approaching is a **warning**, and only *near/at* expiry (or an actual handshake failure)
is a hard **Down**.

**2. But we must not silently regress users who rely on the 30-day page.** So: keep the
existing knob's *meaning* and add a second tier. Map the legacy `thresholdDays` → **`criticalDays`**
(the hard-Down threshold, default stays 30 for back-compat) and introduce **`warningDays`** as a
new, *non-paging* tier (default e.g. 30, or `2 × criticalDays`). Existing rows keep behaving
exactly as today; new behaviour is opt-in via config.

**3. The warning tier is the new first-class `StatusWarning` (depends on
[`2026-06-20-04`](2026-06-20-04-status-warning-degraded.md)).** Warning =
`StatusWarning` (visible as `Degraded`, counts as up for availability, does not open an incident)
plus the same `certExpiryWarning: true` / `severity: "warning"` / `daysRemaining` output for
detail. Critical = `StatusDown` (pages, as today). Actual expiry / handshake failure =
`StatusDown` (as today). **This spec depends on `2026-06-20-04` landing first** — that spec adds
`StatusWarning`, the availability/incident semantics, and the DB constraint. Until then, the
warning tier degrades gracefully to `StatusUp` + the output flag (no amber state).

**4. Compute nearest expiry across the whole presented chain, not just the leaf.** An
intermediate that expires before the leaf is a real, common failure that today's leaf-only
`daysRemaining` misses entirely. Iterate every cert and use the **minimum** days-remaining to
drive the threshold decision; report which cert is the soonest to expire.

Net: same feature you asked for, but the default stops manufacturing fake outages, the warning
tier is a real `Degraded` status (via `2026-06-20-04`), and "chain" means "we check the whole
chain's expiry and report it," because trust is already enforced at handshake.

## Goals

- Two-tier expiry config: `warningDays` (`StatusWarning`/Degraded) and `criticalDays`
  (paging, `StatusDown`), back-compatible with the existing single `thresholdDays`.
- Expiry decision driven by the **minimum days-remaining across the whole presented chain**,
  not just the leaf.
- Output reports the chain: per-cert subject / issuer / notAfter / daysRemaining, the
  soonest-expiring cert, chain length, and whether the chain verified completely.
- The graduated-threshold helper is reusable by `checkdomain`.

## Dependency

- **[`2026-06-20-04` (first-class `StatusWarning`)](2026-06-20-04-status-warning-degraded.md)
  lands first.** It supplies the warning status value, the "warning counts as up / no incident"
  semantics, and the DB constraint. This spec returns `StatusWarning` from the warning tier.

## Out of scope

- The `StatusWarning` plumbing itself — owned by `2026-06-20-04`.
- Per-threshold *notification* fan-out (e.g. a distinct notification at each of 30/14/7/3/1).
  v1 emits one warning flag once the warning tier is crossed; multi-step escalation is the
  existing escalation-policy feature's job.
- Pinning / expected-issuer / CT-log / OCSP-revocation checks.
- Retrofitting `checkdomain` in this spec (share the helper here; wire `checkdomain` to it in a
  follow-up to keep this change focused).

## Design

### Config (`checkssl/config.go`)

```go
type SSLConfig struct {
    Host          string        `json:"host"`
    Port          int           `json:"port"`
    WarningDays   int           `json:"warningDays"`   // new: StatusUp + warning flag
    CriticalDays  int           `json:"criticalDays"`  // new: StatusDown (paging)
    ThresholdDays int           `json:"thresholdDays"` // legacy alias → CriticalDays
    Timeout       time.Duration `json:"timeout,omitempty"`
    ServerName    string        `json:"serverName"`
}
```

Back-compat resolution in `FromMap` / `newExecParams`:
- If `criticalDays` is absent but legacy `thresholdDays` (or snake_case `threshold_days`,
  preserved by the existing `readIntKey` dual-key reader) is present → `criticalDays = thresholdDays`.
- Defaults: `criticalDays` = 30 (matches `defaultThresholdDays`), `warningDays` = 30 as well
  initially so behaviour is unchanged until a user sets a wider warning window; validate
  `warningDays >= criticalDays >= 0`.
- `GetConfig` emits the canonical camelCase keys (and continues to emit `thresholdDays` for
  one release for dashboards that still read it — decision below).

### Execution (`checkssl/checker.go`)

In `buildResult`, after a successful handshake:
1. Iterate `result.state.PeerCertificates` (leaf + presented intermediates). For each, compute
   `daysRemaining`. Track the **minimum** and which cert it belongs to.
2. Also expose chain completeness from `result.state.VerifiedChains` (populated because
   verification ran) — e.g. `chainVerified: len(VerifiedChains) > 0`, `chainLength`.
3. Status decision on the **minimum** days-remaining:
   - `min <= criticalDays` → `StatusDown` (+ `error`, `severity: "critical"`).
   - `criticalDays < min <= warningDays` → `StatusWarning` (visible `Degraded`) +
     `certExpiryWarning: true`, `severity: "warning"`.
   - else → `StatusUp`.
4. Output additions: `chain` (array of `{subject, issuer, notAfter, daysRemaining}`),
   `soonestExpiring` (subject + daysRemaining), `chainLength`, `chainVerified`,
   `certExpiryWarning`, `severity`. Keep existing leaf fields (`subject`, `issuer`, `not_after`,
   `days_remaining`, `serial_number`, `dns_names`) for back-compat. `metrics.days_remaining`
   becomes the **chain minimum** (the meaningful number to graph).

### Shared helper

Extract `gradedExpiryStatus(daysRemaining, warningDays, criticalDays) (Status, severity string)`
into a small shared location (e.g. `checkerdef`) so `checkssl` and later `checkdomain` agree on
the tiering rule. Don't duplicate the comparison literal in two checkers.

## Implementation

1. **`checkssl/config.go`**: add `WarningDays` / `CriticalDays`, legacy `thresholdDays` aliasing,
   validation (`warningDays >= criticalDays >= 0`, both `<=` some sane max), `GetConfig` keys.
2. **`checkerdef`**: add `gradedExpiryStatus` helper + severity constants.
3. **`checkssl/checker.go`**: `newExecParams` reads both tiers; `buildResult` iterates the chain,
   computes min days-remaining + chain report, calls the helper, sets status + output/metrics.
4. **Frontend (`web/dash0`)**: SSL check config form exposes `warningDays` + `criticalDays`
   (keep a single field mapping to `criticalDays` if simpler, with warning as advanced); the
   check-detail result view renders the chain table + an amber "expiring in N days" badge when
   `certExpiryWarning`. Confirm against the design reference at
   `/dash0/orgs/default/design-reference` and reuse existing primitives.
5. **Docs**: update the SSL section of [`docs/api-specification.md`](../../docs/api-specification.md)
   and any SSL feature page under `docs/features/` with the new config keys + output shape.

## Open questions / decisions for the user

1. **Default `warningDays`** — keep it equal to `criticalDays` (30) so behaviour is byte-for-byte
   unchanged until opted-in (safest), or default to something wider like 30/critical-7 so users
   get the better behaviour out of the box (changes default paging)? I lean **safe** (equal),
   with a release note nudging users to widen it.
2. **Hard-Down default** — leave `criticalDays` at **30** (preserves today's paging), or drop the
   recommended default to **7** so a valid cert isn't a month-long "outage"? I'd keep 30 as the
   *default* but document 7 as recommended.
3. **Apply the same tiering to `checkdomain` now or later?** This spec assumes later (shared
   helper here, wire-up follow-up).

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()` per `server/CLAUDE.md`):**
  - Leaf far out, intermediate expiring inside `warningDays` → `StatusUp` + `certExpiryWarning`,
    `metrics.days_remaining` == intermediate's (proves whole-chain min, not leaf).
  - Min days inside `criticalDays` → `StatusDown`, `severity == "critical"`.
  - Min days between the tiers → `StatusUp`, `severity == "warning"`.
  - Min days beyond both → `StatusUp`, no warning flag.
  - Legacy `thresholdDays` only (no `criticalDays`) → behaves exactly as today (Down at 30).
  - `warningDays < criticalDays` → validation error.
  - Use crafted in-memory cert chains (generate test certs with controlled `NotAfter`).
- **Manual:** point an SSL check at a host with a near-expiry intermediate; confirm warning
  badge + chain table render and no false incident is opened.
- `make lint` and `make test` pass; `make test-dash` if the form changes.

## Files referenced

- `server/internal/checkers/checkssl/config.go` — two-tier config + legacy alias
- `server/internal/checkers/checkssl/checker.go` — chain iteration, min-expiry, status, output
- `server/internal/checkers/checkerdef/types.go` — `StatusWarning` (added by `2026-06-20-04`), severity helper home
- `server/internal/checkers/checkdomain/checker.go` — same pattern, share helper (follow-up wire-up)
- `web/dash0/src/routes/**` — SSL config form + check-detail chain rendering
- `docs/api-specification.md`, `docs/features/**` — config keys + output shape
- `docs/competitors/maintenant.md` — source of the requirement
