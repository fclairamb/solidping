# Hyperping — Monitoring Logic

Detection behavior, monitor types, and per-monitor configuration. See [alerting.md](alerting.md) for what happens *after* a failure is detected.

## Monitor types

Hyperping exposes the following protocols (per the Create Monitor API schema and the "Monitoring" docs section):

- **HTTP / HTTPS** — primary type, default `protocol = http`.
- **TCP port** — `protocol = port`, requires `port` field.
- **ICMP ping** — `protocol = icmp`, hostname/IP target.
- **DNS** — `protocol = dns` with `dns_record_type` (A, AAAA, CNAME, MX, NS, TXT, SOA, SRV, CAA, PTR), `dns_nameserver` (e.g. `8.8.8.8`), `dns_expected_answer`.
- **Keyword** — *not* a separate type; it's a flag on an HTTP monitor (`required_keyword`, literal substring).
- **SSL** — *not* a separate monitor; auto-attached to every HTTPS monitor.
- **Browser checks** — Playwright TS/JS scripts; separate quota tier (3/10/25 per plan). See [platform.md](platform.md).
- **Healthchecks** — cron/heartbeat with their own resource and ping endpoints. See [platform.md](platform.md).
- **Server agents** — counted separately on every plan tier (1/5/20/100). Likely a host/agent install for process/disk/CPU monitoring; underdocumented publicly.

No native UDP, multi-step API, gRPC, RUM, queue, or database protocols.

## HTTP check configuration

The Create Monitor API is the authoritative source — the prose docs lag.

- **Methods**: `GET`, `POST`, `PUT`, `HEAD`, `DELETE`, `PATCH`, `OPTIONS` (default `GET`).
- **Custom headers**: `request_headers: [{ name, value }]`.
- **Request body**: `request_body` (string), used for POST/PUT/PATCH.
- **Auth**: not exposed as a structured field. Basic / Bearer auth is done by hand via the `Authorization` header.
- **Expected status code**: `expected_status_code` accepts `"2xx"`, range form `"1xx-3xx"`, or a specific code like `"401"`. Default `"2xx"`.
- **Redirects**: `follow_redirects` (boolean, default `true`).
- **SSL verification**: not exposed. Always on.
- **Keyword**: `required_keyword` — fails the monitor if the literal substring is missing from the response body. No regex or JSON-path support.
- **Max response time / latency threshold**: not a failure condition. Response times are graphed; warn/critical bands (e.g. 2 s / 5 s) are documented as a recommendation, not a configurable monitor field.
- **Request timeout**: not exposed in the API; ~10 s is the recommended ceiling per the guide.
- **`alerts_wait`**: see "Detection logic" below.

## Check intervals

Per-check field `check_frequency` (seconds). Allowed values, from the API enum:

```
10, 20, 30, 60, 120, 180, 300, 600, 1800, 3600, 21600, 43200, 86400
```

Plan caps:

| Plan | Min interval |
|---|---|
| Free | 5 min (300 s) |
| Essentials | 30 s |
| Pro | 30 s |
| Business | 20 s |
| Enterprise | 10 s |

Interval is per-check, not per-account.

## Detection logic — multi-region confirmation

Hyperping's anti-false-positive design is its most distinctive feature. The flow:

1. A single failed probe in the originally-scheduled region triggers detection (carries `original: true` in the payload).
2. The failure is **replayed against every other selected region** before declaring an outage. Replay pings carry `original: false`.
3. **Quorum is 3 regions failing.** 1 or 2 regions failing is treated as a localized network problem and suppressed.
4. Retry timing is 5–15 seconds between primary failure and confirmation pings, varying by check type.
5. The full trace (`pings[]` with `original`, `location`, `status`, `statusMessage`) is embedded in outgoing webhooks — receivers can re-derive the alert decision.

The 3-region threshold is hard-coded. There is no API field to tune it.

### `alerts_wait` — the per-monitor confirmation knob

Independent of the multi-region confirmation. A discrete dropdown:

```
-1, 0, 1, 2, 3, 5, 10, 30, 60
```

Default 0 (alert immediately on confirmed outage). `-1` likely disables alerts entirely. The unit (seconds vs minutes) is not crisply documented; the API treats it as a number.

### Recovery

Monitor-based outages auto-resolve when the monitor recovers. There is **no** documented "must stay up for N seconds" recovery hold equivalent to BetterStack's `recovery_period`. (Notable gap.)

### Recommended config from the official guide

For a typical user-facing API: 1-min interval, 2-region confirmation, 10 s timeout, 2 consecutive failures, response-time warn 2 s / critical 5 s.

### Browser-check "Double Check"

Browser monitors have their own retry mechanism: a failed run is retried immediately from a different region before declaring outage. On by default. Conceptually the same idea as the multi-region confirmation but shaped around the much heavier cost of a browser run.

## Probe locations / regions

18 documented regions (via the API enum):

```
sanfrancisco, nyc, london, paris, frankfurt, seoul, mumbai, bangalore,
saopaulolocal, california, virginia, sydney, toronto, amsterdam,
singapore, tokyo, bahrain, capetown
```

Marketing claims "15+" / "18+ regions across NA, EU, Asia, Oceania". Underlying infra is a mix of AWS + Scaleway + DigitalOcean.

The user picks a subset per monitor via the `regions: []` array. There is **no** "any vs all" toggle — the model is implicitly "any region OK, gated by 3-region confirmation". Status pages and the monitor list aggregate response time **by continent**, not by individual region.

## Sources

- https://hyperping.com/docs/api/monitors/create
- https://hyperping.com/docs/api/monitors/list
- https://hyperping.com/docs/monitoring
- https://hyperping.com/docs/monitoring/create-monitor
- https://hyperping.com/docs/monitoring/ssl
- https://hyperping.com/docs/monitoring/healthchecks
- https://hyperping.com/blog/reduce-false-positive-monitoring-alerts
- https://hyperping.com/guide/uptime-monitoring/check-intervals-and-thresholds
- https://hyperping.com/docs/integrations/webhooks
