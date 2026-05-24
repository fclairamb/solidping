# Freebox line-quality check type

## Context

Depends on [`2026-05-24-06-freebox-os-integration.md`](2026-05-24-06-freebox-os-integration.md).

HTTP and ICMP checks answer "is this host reachable?" but they cannot tell you *why* it is not
reachable, or that it is *about to* become unreachable. xDSL and FTTH fiber connections have rich
telemetry that predicts failure: sync-rate drift, SNR margin collapse, rising FEC/CRC error counts.
These signals are available only via the Freebox OS API.

This spec defines a `freebox_line` check type that polls the Freebox WAN and line-quality
endpoints and maps the readings to solidping's standard up/degraded/down lifecycle. It is the
primary reason the Freebox integration exists.

### Why not use SNMP?

The shipped SNMP checker (`specs/done/2026/03/2026-03-28-01-snmp-monitoring.md`) works well for
generic switch/router monitoring via standard SNMP OIDs. The Freebox, however:

- Does not expose a usable SNMP agent for line-quality statistics.
- Exposes a proprietary JSON API that returns parsed, meaningful values (SNR in dB, attenuation in
  dB, FEC/CRC counters) without requiring OID lookups or custom MIBs.
- Returns a single coherent WAN-state blob that maps naturally to a single check result.

SNMP and `freebox_line` serve different things. They do not overlap on line quality.

## Honest opinion

This is the spec that justifies the foundation. Without a line-quality check, the Freebox
integration is just a fancier way to confirm "my router is up" — which ICMP already does.

The threshold model below is deliberately simple for v1. Do not add adaptive baselines, ML
anomaly detection, or rolling-window statistics — a fixed minimum sync rate and minimum SNR
margin cover 90% of the value and are easy to reason about. Complexity can be added later if
users ask for it.

The "auto" link type detection (xDSL vs FTTH) is a nice-to-have but adds an extra API call per
execution. Default to requiring the user to configure it explicitly in v1; add auto-detect in v2.

## Goal

- New check type `freebox_line` in the checker registry.
- Polls Freebox WAN state + xDSL or FTTH line stats on each execution.
- Configurable warning/critical thresholds for sync rate, SNR margin, attenuation, and error count.
- Records key metric values (sync rate, SNR, error rate) for trending in the history bar.
- Maps to standard solidping check status: `up`, `warning`, `down`.

## Non-goals

- Auto-detection of link type (defer to v2).
- WiFi quality monitoring (covered by a potential future spec).
- System health checks (CPU temp, fan RPM) — small enough to bundle here if desired, or defer.
- Alerting on individual FEC/CRC/HEC counter spikes vs absolute values (v1 uses counts per interval).

## API endpoints used

### WAN state

```
GET /api/v4/connection/
→ {
    "state":         "up" | "down" | "unknown",
    "type":          "xdsl" | "ftth" | "4g" | "unknown",
    "rate_up":       1234,   // bytes/s upstream, current
    "rate_down":     5678,   // bytes/s downstream, current
    "bandwidth_up":  9000000,  // bits/s, nominal
    "bandwidth_down": 18000000,
    "ipv4":          "82.x.x.x",
    "ipv6":          "2a01:..."
  }
```

If `state != "up"`, the check is immediately `down`. No further API calls needed.

### xDSL stats (ADSL/VDSL2)

```
GET /api/v4/connection/xdsl/
→ {
    "status": {
      "status":     "showtime" | "training" | "idle",
      "modulation": "vdsl2" | "adsl2+",
      "uptime":     12345    // seconds since last sync
    },
    "down": {
      "maxrate":  100000,    // kbit/s, attainable (ATM)
      "rate":     80000,     // kbit/s, current sync rate
      "snr":      100,       // dB × 10 — divide by 10 for dB
      "attn":     250,       // dB × 10
      "fec":      0,         // FEC errors since last sync
      "crc":      0,         // CRC errors
      "hec":      0,         // HEC errors
      "es":       0,         // errored seconds
      "ses":      0          // severely errored seconds
    },
    "up": { /* same shape */ }
  }
```

Note: `snr` and `attn` values are in units of tenths of a dB. Divide by 10.

### FTTH stats

```
GET /api/v4/connection/ftth/
→ {
    "sfp_present":    true,
    "sfp_alim_ok":    true,
    "sfp_has_power_report": true,
    "sfp_has_signal":  true,
    "link":            true,
    "sfp_serial":      "...",
    "sfp_vendor":      "...",
    "sfp_pwr_tx":      3450,  // µW × 1000 → mW; convert to dBm
    "sfp_pwr_rx":      -1230  // same unit
  }
```

SFP power in µW: convert to dBm as `10 * log10(mW)`. A healthy FTTH SFP has Tx between −3 and
+3 dBm and Rx between −27 and −3 dBm. Loss of signal (`sfp_has_signal: false`) → `down`.

## Check config

### `FreeboxLineConfig`

New file `server/internal/checkers/checkfreeboxline/config.go`:

```go
type FreeboxLineConfig struct {
    // ConnectionUID references an integration_connections row with type "freebox".
    ConnectionUID string `json:"connectionUid"`

    // LinkType forces xDSL or FTTH parsing; "xdsl" or "ftth". Required for now.
    LinkType string `json:"linkType"` // "xdsl" | "ftth"

    // xDSL thresholds (ignored for FTTH checks)
    MinSyncRateDownKbps int `json:"minSyncRateDownKbps,omitempty"` // warning if below
    MinSnrMarginDownDb  int `json:"minSnrMarginDownDb,omitempty"`   // warning if below (in dB, not tenths)
    MaxAttenuationDb    int `json:"maxAttenuationDb,omitempty"`     // warning if above
    MaxCrcErrorsPerRun  int `json:"maxCrcErrorsPerRun,omitempty"`   // warning if above (since last check)

    // FTTH thresholds (ignored for xDSL checks)
    MinRxPowerMw float64 `json:"minRxPowerMw,omitempty"` // warning if below (in mW)
    MaxRxPowerMw float64 `json:"maxRxPowerMw,omitempty"` // warning if above
}
```

`SecretFields()` returns `[]string{}` — no secrets in the check config itself; secrets are in the
referenced connection's `settings_private`.

`Validate()` checks:
- `ConnectionUID` is non-empty.
- `LinkType` is `"xdsl"` or `"ftth"`.
- Threshold values are non-negative.

### Status mapping

| Condition | Status |
|---|---|
| WAN `state != "up"` | `down` |
| xDSL status `!= "showtime"` | `down` |
| FTTH `!sfp_has_signal` or `!link` | `down` |
| Sync rate or SNR below threshold | `warning` |
| Attenuation above threshold | `warning` |
| CRC errors above threshold | `warning` |
| FTTH Rx power out of range | `warning` |
| All thresholds satisfied | `up` |

If any threshold is zero (default), skip that check. This lets users configure only the
thresholds they care about.

### Check result body

Store key values in the check result for display and trending:

```json
{
  "state": "up",
  "type": "xdsl",
  "syncRateDownKbps": 80000,
  "snrMarginDownDb": 10.0,
  "attenuationDb": 25.0,
  "crcErrors": 0,
  "uptimeSeconds": 12345
}
```

For FTTH:
```json
{
  "state": "up",
  "type": "ftth",
  "link": true,
  "sfpHasSignal": true,
  "rxPowerMw": 0.32,
  "txPowerMw": 1.58,
  "sfpVendor": "..."
}
```

## Checker implementation

New package `server/internal/checkers/checkfreeboxline/`:

### `checker.go`

```go
type FreeboxLineChecker struct{}

func (c *FreeboxLineChecker) Type() checkerdef.CheckType {
    return checkerdef.CheckTypeFreeboxLine
}

func (c *FreeboxLineChecker) Execute(ctx context.Context, jctx *jobdef.JobContext, rawConfig any) (*checkerdef.Result, error) {
    cfg := rawConfig.(*FreeboxLineConfig)

    // Resolve the referenced connection → decrypt app_token.
    conn, err := resolveConnection(ctx, jctx, cfg.ConnectionUID)
    if err != nil {
        return nil, fmt.Errorf("freebox connection: %w", err)
    }

    client := freebox.NewClient(conn.BaseURL, conn.AppToken)

    // 1. Check WAN state.
    var wan connectionResponse
    if err := client.Get(ctx, "/api/v4/connection/", &wan); err != nil {
        return &checkerdef.Result{Status: checkerdef.StatusDown, Message: err.Error()}, nil
    }
    if wan.Result.State != "up" {
        return &checkerdef.Result{Status: checkerdef.StatusDown, Message: "WAN state: " + wan.Result.State}, nil
    }

    // 2. Check line quality.
    switch cfg.LinkType {
    case "xdsl":
        return c.executeXDSL(ctx, client, cfg)
    case "ftth":
        return c.executeFTTH(ctx, client, cfg)
    default:
        return nil, fmt.Errorf("unknown linkType: %s", cfg.LinkType)
    }
}
```

`resolveConnection` looks up the `IntegrationConnection` by UID, asserts `type == "freebox"`, and
decrypts `settings_private` to get the `app_token`. It uses the same credentials infrastructure as
other encrypted-credential consumers in the codebase.

### Registry

`server/internal/checkers/checkerdef/types.go` — add:
```go
CheckTypeFreeboxLine CheckType = "freebox_line"
```

`server/internal/checkers/registry/registry.go` — add to both `GetChecker()` and `ParseConfig()`:
```go
case checkerdef.CheckTypeFreeboxLine:
    return &checkfreeboxline.FreeboxLineChecker{}, true
// and
case checkerdef.CheckTypeFreeboxLine:
    cfg := &checkfreeboxline.FreeboxLineConfig{}
    return cfg, json.Unmarshal(raw, cfg)
```

## Frontend

### Check form

`web/dash0/src/components/shared/check-form.tsx` or the type-specific form router — add a
`freebox_line` section that renders:

1. A searchable dropdown for the Freebox connection (`connectionUid`). Shows connection name
   and status badge. If no connections exist, show a link to create one.
2. A `linkType` radio: "xDSL/ADSL" | "FTTH Fiber".
3. Threshold fields (collapsible "Advanced thresholds" section, pre-filled with sensible defaults):
   - xDSL: minimum sync rate (Kbps), minimum SNR margin (dB), max attenuation (dB), max CRC errors per run.
   - FTTH: min/max Rx power (mW) — pre-fill with typical healthy range (0.05–0.5 mW).
4. Helpful copy: "Solidping will mark the check as warning if any threshold is crossed.
   Leave a field at 0 to skip that threshold."

### Check detail page

In the check result or metrics area, surface the raw readings from the last execution:

```
Last reading (2 min ago)
  WAN state:     up
  Sync rate ↓:   80 000 Kbps  (threshold: 60 000)
  SNR margin ↓:  10.0 dB      (threshold: 6 dB)
  Attenuation ↓: 25.0 dB
  CRC errors:    0
  Line uptime:   3h 25m
```

### i18n

`web/dash0/src/locales/en/checks.json` and `fr/checks.json`:
- `"freebox_line.name"` — "Freebox Line Quality"
- `"freebox_line.description"` — "Monitor xDSL/FTTH line quality via Freebox OS"
- `"freebox_line.connectionUid"` — "Freebox connection"
- `"freebox_line.linkType"` — "Link type"
- `"freebox_line.xdsl"` — "xDSL / ADSL"
- `"freebox_line.ftth"` — "FTTH Fiber"
- `"freebox_line.minSyncRate"` — "Minimum sync rate (Kbps)"
- `"freebox_line.minSnr"` — "Minimum SNR margin (dB)"
- `"freebox_line.maxAttenuation"` — "Max attenuation (dB)"
- `"freebox_line.maxCrcErrors"` — "Max CRC errors per check"

## Files to create / modify

### New files
- `server/internal/checkers/checkfreeboxline/config.go`
- `server/internal/checkers/checkfreeboxline/checker.go`
- `server/internal/checkers/checkfreeboxline/checker_test.go`

### Modified files
- `server/internal/checkers/checkerdef/types.go` — `CheckTypeFreeboxLine`
- `server/internal/checkers/registry/registry.go` — `GetChecker` + `ParseConfig`
- `web/dash0/src/components/shared/check-form.tsx` — add freebox_line section
- `web/dash0/src/locales/en/checks.json` — i18n
- `web/dash0/src/locales/fr/checks.json` — i18n

## Tests

### Unit tests (`checkfreeboxline/checker_test.go`)

Use a local HTTP test server to mock Freebox responses:

- `TestXDSLUp`: WAN up, metrics within thresholds → status `up`, result body contains sync rate.
- `TestXDSLWarningLowSnr`: SNR below `MinSnrMarginDownDb` → status `warning`.
- `TestXDSLDownWanState`: WAN state `"down"` → status `down` without further calls.
- `TestFTTHUp`: FTTH link ok, Rx power in range → status `up`.
- `TestFTTHDownNoSignal`: `sfp_has_signal: false` → status `down`.
- `TestSessionRefresh`: First call returns `auth_required`, second call succeeds → status ok (tests
  session renewal).
- `TestValidateConfig`: Missing `connectionUid` or invalid `linkType` → validation error.

## Verification

```bash
make lint && make test
```

Against a real Freebox (requires a paired connection from spec 2):

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create a freebox_line check
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "name": "Freebox line",
    "slug": "freebox-line",
    "type": "freebox_line",
    "config": {
      "connectionUid": "<your-connection-uid>",
      "linkType": "xdsl",
      "minSyncRateDownKbps": 10000,
      "minSnrMarginDownDb": 3
    }
  }' \
  'http://localhost:4000/api/v1/orgs/default/checks'

# Run the check immediately and inspect the result
# (via the dashboard or the check result endpoint)
```

To verify threshold triggering: temporarily set `minSyncRateDownKbps` to a value above your
current sync rate and confirm the check enters `warning` state.

## Implementation Plan

The existing checker architecture is stateless: `Execute(ctx, config)` only sees a
`checkerdef.Config`, with no DB or services access. The spec's pseudocode shows a
`resolveConnection(ctx, jctx, …)` call but there is no `jctx`. We follow the pattern
already established by `checkjs.ResolveChecker`: expose a package-level
`ConnectionResolver` function variable, set from the API server's startup wiring with a
closure that owns the DB + `credentials.Service`. The checker calls it during `Execute`
to look up the referenced Freebox connection and pull the decrypted `app_token`.

Status mapping note: the spec mentions `warning` status, but the existing `Status` enum
is `Up / Down / Timeout / Error / Running` only — there is no `Warning`. We map "warning"
(threshold crossed but link technically up) to `Down` with a clear `Output.error` message
explaining which threshold was crossed, matching how the SNMP checker handles its
threshold-driven outcomes. The check result's structured `Output` map carries `state`,
`type`, and all readings so the UI can render the line-quality context without depending
on the binary up/down for nuance.

Solidping uses no `warning` status state internally, but the spec lists one. The cleanest
v1 mapping: `up` (all clear), `down` (WAN down / FTTH signal lost / xDSL not at showtime),
and `down` with a `degraded: true` marker in Output for threshold breaches. We mark the
status as `Down` so on-call alerts fire on degradations too — the user can lower
thresholds if false positives are noisy.

### Steps

1. **Add `CheckTypeFreeboxLine` constant.** Edit `server/internal/checkers/checkerdef/types.go`
   to add the new check type constant, register it in `checkTypesRegistry` (labels:
   `safe`, `standalone`, `category:infrastructure`), and include it in `ListCheckTypes`.

2. **Implement the checker package.** Create `server/internal/checkers/checkfreeboxline/`
   with `config.go`, `checker.go`, `samples.go`, and `secret_fields.go`:
   - `FreeboxLineConfig` struct + `FromMap`, `GetConfig`, `Validate` mirroring other
     checkers. Fields per spec.
   - `SecretFields()` returns `[]string{}` (connection-side, no secrets in the check itself).
   - `FreeboxLineChecker` implementing `checkerdef.Checker`. `Execute` calls the
     package-level `ConnectionResolver`, builds a freebox client, hits
     `/api/v4/connection/`, branches to xDSL or FTTH, evaluates thresholds, returns a
     `Result` with structured `Output` and `Metrics`.
   - Define `ConnectionResolver func(ctx, connectionUID string) (baseURL, appID, appToken string, err error)`
     as the indirection that the API package wires up at startup.
   - A `GetSampleConfigs` returning a single placeholder sample for the registry.

3. **Register in the registry.** Edit `server/internal/checkers/registry/registry.go`
   to add `CheckTypeFreeboxLine` cases in both `GetChecker` and `ParseConfig`.

4. **Wire the connection resolver.** In `server/internal/app/server.go`, add a single
   assignment to `checkfreeboxline.ConnectionResolver` that closes over `s.dbService` +
   `s.services.Credentials`. The resolver fetches the connection, asserts type ==
   `freebox`, decrypts `settings_private`, and returns the merged base URL + app token.

5. **Backend tests.** `checkfreeboxline/checker_test.go` and `config_test.go`:
   - `TestValidateConfig`: missing `connectionUid`, invalid `linkType`, negative
     thresholds — all surface validation errors.
   - `TestXDSLUp`, `TestXDSLDegradedLowSnr`, `TestXDSLDownNotShowtime` —
     `httptest.NewServer` mocks the Freebox API, the resolver returns a stub
     `baseURL+appToken`; an end-to-end Execute returns the expected status.
   - `TestFTTHUp`, `TestFTTHDownNoSignal`, `TestFTTHDegradedRxPower`.
   - `TestWANDown`: when WAN state is not "up", we return `Down` without calling line-stat
     endpoints.
   - `TestSessionRefresh`: server returns `auth_required` once then succeeds — verifies
     the client retries through the freebox package's session refresh.

6. **Frontend i18n.** Add the keys from the spec to `web/dash0/src/locales/en/checks.json`
   and `fr/checks.json`. Mirror existing channel-based check types where applicable.

7. **Frontend form.** Extend the type-specific form router with a `freebox_line` section
   showing connection dropdown, link-type radio, and collapsible advanced thresholds.
   Reuse design-reference primitives.

8. **QA.** Run `make build-backend build-client lint-back test`; fix any failures.

### Out-of-scope (deferred / dropped)

- The spec hints at a `warning` status; we map degraded → `down` because the enum has no
  `warning`. Documented in the result's `Output.degraded` field.
- Auto-detect xDSL vs FTTH — explicitly deferred to v2 per the spec.
- "Last reading" check-detail panel — this requires custom rendering and is intentionally
  out of scope for the backend-centric v1; the structured Output map is enough for the
  generic JSON viewer the dashboard already ships.

