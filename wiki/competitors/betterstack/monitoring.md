# BetterStack — Monitoring Logic

How BetterStack decides "is this monitor down?" — confirmation, recovery, regions, and the implicit state machine. The API field surface lives in [api.md](api.md); this page is about behavior.

## Detection logic — `confirmation_period`

BetterStack's primary anti-false-positive knob:

- **Definition**: "How long should we wait after observing a failure before we start a new incident."
- **Unit**: integer seconds (wall clock), **not** a count of failed checks. This decouples alert delay from `check_frequency`.
- A confirmation period of **0** ("Immediate") is allowed; the docs example uses `30 s` check frequency + immediate confirmation.
- The set of allowed values surfaces only as a UI dropdown — Immediate, 30 s, 1 min, 2 min, 3 min, 5 min, 10 min — but the API is a free integer.
- **Default in observed payloads**: `confirmation_period: 120` (2 minutes). The docs do not commit to a default in writing.
- The check itself does **not** retry inside a single tick. The "second chance" is the *next region's check*, gated by the confirmation window.

### Multi-region quorum (the second leg)

The `confirmation_period` is wall-clock; the *qualitative* check is regional:

> "Every monitor is checked from at least 4 different locations, and to prevent false positives, an incident is created only after a check fails from at least 3 locations."

Key facts:

- The quorum is **hardcoded at 3 locations**. Not user-configurable.
- 1 or 2 locations failing → no incident; behavior of those failing-but-below-threshold locations is **not exposed** as a partial-down state.
- If a customer narrows `regions` to fewer than 3, the quorum can never be reached. This corner case is undocumented.
- The 3-of-N is independent of `confirmation_period` — both must be satisfied.

## The `validating` state

The monitor `status` enum is `up`, `down`, `validating`, `paused`, `pending`, `maintenance`. The transient **`validating`** state is what the monitor occupies during the confirmation window — externally visible. Users see "this is failing, we're confirming" rather than guessing why no incident opened. **Worth borrowing.**

## Recovery — `recovery_period`

- **Definition**: "How long the monitor must be up to automatically mark an incident as resolved after being down. In seconds."
- **Default in observed payloads**: `0` (instant resolve on first success).
- **Flapping resets the timer.** Quote from the docs:
  > "If any of the checks fails during the recovery period, the counter is reset and the monitor is waiting once more for a valid check."
- Worked example for 30 s frequency / 1 min recovery: incident opens immediately; then **two consecutive 30 s checks must succeed** before auto-resolve. One failure inside the window restarts the count.
- Recovery generates a **`recovery` notification** distinct from the original alert. Incidents move `started_at → acknowledged_at → resolved_at`; `resolved_by` records either the user who clicked Resolve or the system.
- The same field exists on **incoming-webhook integrations**, with identical flap-reset semantics — used when alerts arrive from external sources (Datadog, Prometheus, etc.) instead of native monitors.

## Region selection

- `regions` is a list of strings, allowed values **`["us", "eu", "as", "au"]`** or any subset. Exactly **four logical buckets**, no per-city granularity in the API.
- The IP allowlist exposed in the FAQ (8 IPs in AS, 4 in AU, 4 in EU, 5 in US) hints at multiple PoPs per bucket internally — the `regions` field is the user-facing abstraction.
- Each region runs its own checker pool. Docs only promise the configured `check_frequency` per location, not synchronized ticks across regions.
- The Monitor Response Times API groups response time series by region (`us`, `eu`, …) with sub-fields `at`, `response_time`, `name_lookup_time`, `connection_time`, `tls_handshake_time`, `data_transfer_time` — confirming each region is checked independently.
- **`ip_version`** (`ipv4` / `ipv6`) is a per-monitor field, not per-region. Added late 2024.

## Incident creation

An incident is opened by exactly one of:

- **Monitor failure** — after `confirmation_period` elapses with the 3-of-N quorum.
- **Heartbeat miss** — when `now - last_heartbeat > period + grace`. See [platform.md](platform.md#heartbeats).
- **Incoming webhook / manual API** — `POST /api/v3/incidents` or via an `incoming_webhook` integration.

A single incident object has timing fields:

- `started_at` — incident-open (i.e. *after* `confirmation_period`, not the first failed result)
- `acknowledged_at`, `resolved_at`
- Actor fields: `acknowledged_by`, `resolved_by`

**Notable**: there is no field for "time first failure observed". This means the on-call SLA clock starts at incident-open, not first-failure. SolidPing should expose both timestamps.

Cause is captured in:
- Free-text `cause`
- `response_content`, `response_url`, `screenshot_url`
- `regions` array (which regions were failing when the incident opened)

## Sources

- https://betterstack.com/docs/uptime/confirmation-and-recovery-period/
- https://betterstack.com/docs/uptime/locations-and-regions/
- https://betterstack.com/docs/uptime/working-with-incidents/
- https://betterstack.com/docs/uptime/api/list-all-existing-monitors/
- https://betterstack.com/docs/uptime/api/incidents-api-response-params/
- https://betterstack.com/docs/uptime/api/list-all-incidents/
- https://betterstack.com/docs/uptime/api/get-monitors-response-times/
- https://betterstack.com/docs/uptime/frequently-asked-questions/
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_monitor.md
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_incoming_webhook.md
