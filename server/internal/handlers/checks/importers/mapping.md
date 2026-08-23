# Third-party importer mapping tables

Reference for what each converter in this package maps, and what it reports as
a warning instead. Warnings never block an import: what maps is imported, the
rest is surfaced to the user.

Shared rules (all sources):

- **Slugs** are derived from the source name (`slugify`, deduped with `-2`,
  `-3`, … suffixes) so a re-import upserts by slug and stays idempotent.
- **Out-of-range values are clamped, never rejected** (`normalizeChecks`), each
  with a warning, so a foreign value SolidPing does not allow degrades instead
  of failing the whole check as an `ImportError`:
  - the check **period** is raised to the type's `MinPeriod` (an ssl check
    polled every 30s becomes the SolidPing default);
  - the uniform per-check **`timeout`** key is clamped into 1s–30s
    (`checks.Min/MaxConfigTimeout`). This key is type-agnostic — the checks
    service range-checks it for every type and the worker reads it straight off
    the config map to size the execution budget
    (`checkworker.perCheckTimeout`), so it is meaningful even for checkers
    whose own config struct has no `Timeout` field. Uptime Kuma's 48s default
    timeout lands here on essentially every import;
  - **`confirmationPeriodSeconds`** / **`recoveryPeriodSeconds`** are clamped to
    `checks.MaxIncidentPeriodSeconds` (86400). Better Stack routinely allows
    multi-day heartbeat grace periods, and Kuma's
    `maxretries × retryInterval` product easily exceeds a day.
- **Credentials are never imported — deliberately.** SolidPing *does* have
  somewhere to put them (`checkhttp.HTTPConfig.BasicAuth`, and the
  `Username`/`Password` fields on the ssh, mqtt and database checkers); the
  values are dropped as a security policy, not for lack of a field. A foreign
  export can carry plaintext secrets, and an import must not silently
  re-persist them into SolidPing behind the operator's back. Every dropped
  credential is reported so the operator re-enters it deliberately. The single
  exception is Better Stack request headers, which are carried over into the
  **encrypted** `secretHeaders` field rather than the queryable public config.
- **Notification/alert bindings are never imported** (same as native import).
- The converted document's `organization` is the source id, which becomes the
  managed-manifest label (`solidping.io/managed=gatus|betterstack|uptime-kuma`).

## Gatus (`config.yaml`)

### Check type, from `endpoints[].url`

| Gatus | SolidPing |
|---|---|
| `http://`, `https://` | `http` |
| `ws://`, `wss://`, `websocket://` | `websocket` |
| `tcp://` | `tcp` |
| `tls://`, `starttls://` | `tcp` with `tls: true` |
| `udp://` | `udp` |
| `icmp://`, `ping://` | `icmp` |
| `ssh://` | `ssh` |
| a `dns:` block (url = resolver) | `dns` |
| `sctp://`, anything else | *skipped* + warning |

### Fields

| Gatus | SolidPing |
|---|---|
| `name` | `name` (+ derived `slug`) |
| `group` | `group` (auto-created on apply) |
| `enabled` | `enabled` |
| `interval` | `period` (default `60s`) |
| `method`, `body`, `headers` | http `method` / `body` / `headers` |
| `client.timeout` | checker `timeout` |
| `dns.query-name` / `dns.query-type` | dns `host` / `record_type`; `url` → `nameserver` (`:53` appended) |
| `ssh.username` / `ssh.password` | *not imported* + warning |
| `client.insecure`, `client.ignore-redirect`, `client.dns-resolver` (non-dns) | warning |
| `alerts` | warning |
| `external-endpoints` | warning (recreate as heartbeat checks) |

### Conditions

| Gatus condition | SolidPing |
|---|---|
| `[STATUS] == 200` | `expected_status` |
| `[STATUS] == any(200, 301)` / `== 2XX` | `expected_status_codes` |
| `[STATUS] < 300` (range operators) | warning |
| `[BODY] == value` | `body_expect` |
| `[BODY] != value` | `body_reject` |
| `[BODY] == pat(*glob*)` | `body_pattern` (glob → anchored regex) |
| `[BODY] != pat(...)` | `body_pattern_reject` |
| `[BODY].path <op> value` | a `json_path_assertions` leaf (`eq`/`neq`/`gt`/`gte`/`lt`/`lte`; `pat()` → `regex`) |
| `has([BODY].path) == true/false` | `exists` / `not_exists` assertion |
| `len([BODY].path) …` | warning |
| `[CERTIFICATE_EXPIRATION] > 48h` | an extra `ssl` check (`criticalDays`/`warningDays`) + warning |
| `[DOMAIN_EXPIRATION] > 720h` | an extra `domain` check (`thresholdDays`) + warning |
| `[BODY] == 1.2.3.4` on a dns endpoint | `expected_values` |
| `[CONNECTED] == true`, `[DNS_RCODE] == NOERROR` (dns) | implicit, no-op |
| `[RESPONSE_TIME]`, `[IP]`, anything else | warning |

Multiple body-path conditions are combined under a single `and` assertion node.

## Uptime Kuma (backup JSON, 1.x)

Kuma 2.x removed the JSON backup export; 2.x users need a 1.x-compatible
export. A backup with no `monitorList` is rejected with that hint.

### Monitor types

| Kuma `type` | SolidPing |
|---|---|
| `http` | `http` |
| `keyword` | `http` + `body_expect` (or `body_reject` when `invertKeyword`) |
| `json-query` | `http` + `json_path_assertions` (`jsonPath` + `expectedValue`, `eq`) |
| `port` | `tcp` |
| `ping` | `icmp` |
| `dns` | `dns` |
| `docker` | `docker` (`docker_container` → `containerName`; the Docker host warns) |
| `push` | `heartbeat` + warning (the ping URL changes) |
| `grpc-keyword` | `grpc` |
| `mqtt` | `mqtt` |
| `postgres` | `postgresql` |
| `mysql` | `mysql` |
| `redis` | `redis` |
| `sqlserver` | `mssql` |
| `mongodb` | `mongodb` |
| `steam` | `a2s` |
| `real-browser` | `browser` |
| `group` | a **check group** (children reference it via `parent`), not a check |
| `gamedig`, `radius`, `tailscale-ping`, … | *skipped* + warning |

### Fields

| Kuma | SolidPing |
|---|---|
| `name`, `description` | `name` (+ derived `slug`), `description` |
| `active` | `enabled` |
| `interval` | `period` |
| `maxretries` × `retryInterval` | `confirmationPeriodSeconds` (the export format replaced the old `incidentThreshold` count with a duration) |
| `timeout` | checker `timeout` |
| `accepted_statuscodes` | `expected_status_codes` (`200-299` → `2XX`; other ranges warn) |
| `headers` (JSON string) | http `headers` |
| `databaseConnectionString` | host / port / username / database (password dropped + warning) |
| `databaseQuery` | `query` (postgres/mysql/mssql only) |
| `dns_resolve_server` / `dns_resolve_type` | `nameserver` (`:53` appended) / `record_type` |
| `mqttTopic` / `mqttUsername` | `topic` / `username` (password + success message warn) |
| `grpcUrl` / `grpcServiceName` / `grpcEnableTls` / `keyword` | `host`+`port` / `serviceName` / `tls` / `keyword` |
| `ignoreTls`, `upsideDown`, `authMethod`, `tags`, `notificationIDList` | warnings |

## Better Stack (Uptime API)

The server fetches every page of `GET /api/v2/monitors` **and**
`GET /api/v2/heartbeats` with the supplied token as a Bearer credential. The
token is never persisted, logged, or echoed in an error, upstream error bodies
are never surfaced (they can echo the request back), and a pagination cursor
pointing at a different host aborts the fetch.

### Monitor types

| `monitor_type` | SolidPing |
|---|---|
| `status`, `expected_status_code` | `http` |
| `keyword` | `http` + `body_expect` |
| `keyword_absence` | `http` + `body_reject` |
| `ping` | `icmp` |
| `tcp` | `tcp` |
| `udp` | `udp` |
| `smtp` | `smtp` |
| `pop` | `pop3` |
| `imap` | `imap` |
| `dns` | `dns` |
| `playwright`, anything else | *skipped* + warning |

### Fields

| Better Stack | SolidPing |
|---|---|
| `pronounceable_name` | `name` (+ derived `slug`) |
| `paused` | `enabled: false` |
| `check_frequency` (seconds) | `period` |
| `request_timeout` (seconds) | checker `timeout` |
| `http_method`, `request_body` | http `method`, `body` |
| `request_headers` | http `headers`; credential-looking names → encrypted `secretHeaders` |
| `required_keyword` | `body_expect` / `body_reject` |
| `expected_status_codes` | `expected_status_codes` |
| `port` (number or string) | checker `port` |
| `verify_ssl: false`, `follow_redirects: false` | warnings |
| `auth_username` / `auth_password` | *not imported* + warning |
| `monitor_group_id`, `ssl_expiration`, `domain_expiration` | warnings |

### Heartbeats

| Better Stack | SolidPing |
|---|---|
| `name` | `name` (+ derived `slug`) |
| `period` | `period` (the expected interval) |
| `grace` | `confirmationPeriodSeconds` |
| `paused` | `enabled: false` |

A document-level warning always reminds the operator that SolidPing issues its
own ping URLs, so every cron job or agent must be repointed after the import.

## UptimeRobot (API v2 `getMonitors` JSON)

Users fetch the JSON themselves with a read-only API key (see the migration
guide for the exact `curl`) and paste or upload it — there is no outbound
fetch here, unlike Better Stack. Three input shapes are accepted so users can
paste whatever their export produced: the raw response object
(`{"stat":"ok","pagination":{...},"monitors":[...]}`), a bare `monitors`
array, or an array of page objects (concatenated paginated responses,
disambiguated from a bare monitors array by checking whether the first
element carries a `monitors` key).

### Monitor types

| UptimeRobot `type` | SolidPing |
|---|---|
| `1` HTTP(s) | `http` |
| `2` Keyword | `http` + content assertion (`keyword_type: 1` exists → `body_expect`, `2` not-exists → `body_reject`, using `keyword_value`) |
| `3` Ping | `icmp` |
| `4` Port | `tcp` (`sub_type` well-known 1-6 → HTTP/HTTPS/FTP/SMTP/POP3/IMAP's standard port; `99` or unset → the `port` field; any other value falls back to `port` with a warning) |
| `5` Heartbeat | `heartbeat` + warning (the ping URL changes, same as Kuma's `push`) |
| anything else | *skipped* + warning |

### Fields

| UptimeRobot | SolidPing |
|---|---|
| `friendly_name` (falls back to `url`) | `name` (+ derived `slug`) |
| `interval` | `period` |
| `timeout` | checker `timeout` |
| `status: 0` (paused) | `enabled: false` |
| `http_auth_type: 1` (basic) + `http_username`/`http_password` | `basicAuth` — **imported**, unlike every other converter's credential policy (see below) |
| `alert_contacts`, `mwindows` | warning (once per document) |
| `custom_http_statuses`, `custom_http_headers`, `psp` | warning (per monitor, when non-empty) |

UptimeRobot's `sub_type` and `port` fields are decoded through a small
`robotFlexInt` type because the API renders them inconsistently as either a
JSON number or a numeric string.

**Credential policy exception.** Every other converter in this package
deliberately drops credentials (see the shared rules above). UptimeRobot is
the one exception: a **basic** HTTP auth credential (`http_auth_type: 1`) is
carried into the check's encrypted `basicAuth` field, per this converter's
spec. Digest auth (`http_auth_type: 2`) and any other scheme are *not*
imported and warn instead.
