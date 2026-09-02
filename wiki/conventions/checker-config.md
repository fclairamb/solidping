# Checker Configuration Reference

Complete reference for all 39 checker types and their configuration fields. Field names shown are the JSON keys used in the `config` object of a check.

**Legend**: (R) = required, (O) = optional. Duration fields accept Go duration strings (e.g., `"10s"`, `"1m"`).

---

## Shared keys

A few config keys are **type-agnostic**: they are read generically off the raw
config map rather than being fields on a checker's config struct, so they are not
repeated in the per-type tables below.

| Field | Type | O/R | Default | Description |
|-------|------|-----|---------|-------------|
| `timeout` | duration | O | `15s` | Per-check execution budget, 1s-30s. Overrides the cost-aware clamp. |
| `tunnelCheckUid` | string | O | | Dial the probe through an `ssh` check's bastion. Only on types whose `supportsTunnel` metadata is true. |
| `ipVersion` | string | O | `auto` | Address family to probe over: `auto` / `ipv4` / `ipv6`. Read fallback: `ip_version`. Only on types whose `supportsIpVersion` metadata is true (`http`, `tcp`, `udp`, `icmp`, `ssl`, `ssh`, `smtp`, `imap`, `pop3`, `dnsbl`). |

`ipVersion` notes — `auto` is the historical behaviour (probe one address,
IPv4-first for the address-picking checkers, Happy Eyeballs for `http`) and is
byte-for-byte unchanged. A pinned family never falls back: a target with no
address of that family fails with `checkerdef.ErrNoAddressForFamily`, and a
worker with no egress for it fails with `checkerdef.ErrWorkerNoEgress` (which
blames the worker, not the target). `dns` rejects the key outright (the two
plausible meanings are different features); `dnsbl` accepts only `auto`/`ipv4`;
and a tunneled check rejects it, because the bastion resolves and dials. The
family actually used is reported as the `ip_version` result-output field. Address
selection lives in exactly one place, `checkerdef.SelectIPAddr` — no checker
keeps its own IPv4-preference loop.

---

## Network

### `http` -- HTTP/HTTPS endpoint monitoring

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | R | | URL to check (http:// or https://) |
| `method` | string | O | GET | HTTP method (GET, POST, etc.) |
| `headers` | map[string]string | O | | HTTP headers to send |
| `body` | string | O | | Request body |
| `username` | string | O | | Basic auth username |
| `password` | string | O | | Basic auth password |
| `expected_status` | int | O | | Expected status code (deprecated, use `expected_status_codes`) |
| `expected_status_codes` | string[] | O | ["2XX"] | Expected status code patterns (e.g., `["200", "2XX"]`) |
| `body_expect` | string | O | | String that must be found in response body |
| `body_reject` | string | O | | String that must NOT be found in response body |
| `body_pattern` | string | O | | Regex pattern that must match in response body |
| `body_pattern_reject` | string | O | | Regex pattern that must NOT match in response body |
| `headers_pattern` | map[string]string | O | | Map of header name to regex pattern; all must match |
| `json_path_assertions` | object | O | | AST-based JSONPath assertions (see below) |
| `verifySsl` | bool | O | true | Verify the TLS certificate. `false` skips verification (`InsecureSkipVerify`) and marks the result with `tls_verify_skipped: true`. Read fallback: `verify_ssl` |
| `followRedirects` | bool | O | true | Follow HTTP redirects (up to 10). `false` stops at the first response, so status/body/header assertions run against the redirect itself (e.g. `expectedStatus: 301` + `headersPattern.Location`). Read fallback: `follow_redirects` |
| `capture_failure_response` | bool | O | false | Keep what the probe received when the check FAILS, as incident diagnostics (spec 2026-08-20-01). See below. Read fallback: `captureFailureResponse` |

**Failure-response capture** (`capture_failure_response`):

Opt-in, default off. On a **failed** execution that actually received a
response, the checker keeps the status line, the response headers and a
16 KiB-capped body, and the incident pipeline persists it on the incident this
failure opens (or reopens) as `details.failureResponse`. Nothing is captured on
a success, nor when the request failed before any response existed (timeout,
DNS, TLS) — the existing error output already covers those.

- **Privacy — this is why it is opt-in.** A response body can contain PII,
  session material, internal hostnames or stack traces. The capture is
  operator-only: it lives on the incident, is visible to the org, and is
  **never** serialized onto a status page, its `activeIncidents[]` block, or a
  subscriber payload. Enable it per check, deliberately.
- Sensitive **response** header values (`Set-Cookie`, `Authorization`,
  `Proxy-Authenticate`, `WWW-Authenticate`, and any header name containing
  `token`/`key`/`secret`) are replaced with `[redacted]`. **Request** headers are
  never captured at all — they carry the check's own credentials.
- A body that is not text-like by content type, or not valid UTF-8, is reduced
  to metadata (content type, size, SHA-256); raw bytes are never stored.
- The capture rides a dedicated `diagnostics` field, not the result `output`
  map, so it never lands in the `results` table.
- It is **observationally inert on the verdict**: it reuses bytes the probe
  already read and never feeds the assertion path, so toggling it can never move
  a check between up and down.

**JSONPath assertion node** (`json_path_assertions`):

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Node type: `"assertion"`, `"and"`, or `"or"` |
| `path` | string | JSONPath expression (for `assertion` type) |
| `operator` | string | Comparison operator (for `assertion` type) |
| `value` | string | Expected value (for `assertion` type) |
| `children` | node[] | Child assertion nodes (for `and`/`or` types) |

The assertions open the response-body read on their own — no `body_*` key is
needed alongside them. (Before spec 2026-08-20-04 they did not: a check
configured with only `json_path_assertions` never read the body and therefore
never evaluated them, reporting up regardless of the payload.) They are still
skipped when the response body is **empty**; a non-JSON body fails the check
with `response body is not valid JSON for assertion evaluation`.

---

### `tcp` -- TCP port connectivity

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | O | | TCP URL (e.g., `tcp://host:port` or `tcps://host:port`) |
| `host` | string | O | | Hostname (legacy, use `url` instead) |
| `port` | int | O | | Port number (legacy, use `url` instead) |
| `timeout` | duration | O | | Connection timeout |
| `send_data` | string | O | | Data to send after connecting |
| `expect_data` | string | O | | Expected data in response |
| `tls` | bool | O | false | Use TLS/SSL |
| `tls_verify` | bool | O | false | Verify TLS certificate |
| `tls_server_name` | string | O | | Override TLS server name (SNI) |

Either `url` or `host`+`port` is required.

---

### `udp` -- UDP port reachability

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Target hostname |
| `port` | int | R | | Target port |
| `timeout` | duration | O | | Response timeout |
| `send_data` | string | O | | Data to send |
| `expect_data` | string | O | | Expected response data |

---

### `icmp` -- ICMP ping

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | O | | ICMP URL (e.g., `ping://host` or `icmp://host`) |
| `host` | string | O | | Target hostname (legacy, use `url` instead) |
| `timeout` | duration | O | | Ping timeout |
| `count` | int | O | | Number of ping packets |
| `interval` | duration | O | | Interval between packets |
| `packet_size` | int | O | | Packet size in bytes |
| `ttl` | int | O | | Time to live |

Either `url` or `host` is required. Requires raw socket privileges.

---

### `dns` -- DNS resolution

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | O | | DNS URL (e.g., `dns://resolver/domain?type=A`) |
| `host` | string | O | | Domain to query (legacy, use `url` instead) |
| `timeout` | duration | O | | Query timeout |
| `nameserver` | string | O | system resolver | DNS server to use |
| `record_type` | string | O | | Record type: A, AAAA, MX, CNAME, TXT, etc. |
| `expected_ips` | string[] | O | | Expected IP addresses in response |
| `expected_values` | string[] | O | | Expected values in response records |

Either `url` or `host` is required. Default period: 5 minutes.

---

### `websocket` -- WebSocket connectivity

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | R | | WebSocket URL (ws:// or wss://) |
| `headers` | map[string]string | O | | HTTP headers for handshake |
| `send` | string | O | | Text message to send after connecting |
| `expect` | string | O | | Regex pattern to match against received message |
| `tls_skip_verify` | bool | O | false | Skip TLS certificate verification |
| `timeout` | duration | O | 10s | Maximum time for entire check (max: 60s) |

---

### `grpc` -- gRPC health check

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Target hostname |
| `port` | int | O | 50051 | Target port. Validation: 1-65535 |
| `tls` | bool | O | false | Use TLS |
| `tlsSkipVerify` | bool | O | false | Skip TLS certificate verification |
| `serviceName` | string | O | | gRPC service name for health check. Empty = overall server health |
| `timeout` | duration | O | 10s | Check timeout (max: 60s) |
| `metadata` | map[string]string | O | | Request metadata sent on the health RPC. Stays in the public, queryable config |
| `secretMetadata` | map[string]string | O | | Same, but declared in `SecretFields()` — split out of the public config and encrypted at rest, never returned by the API |
| `keyword` | string | O | | Keyword to search for in response (deprecated — matches the serving-status enum the check already evaluates; not offered in the dashboard) |
| `invertKeyword` | bool | O | false | Fail if keyword IS found (instead of not found) (deprecated, see `keyword`) |

Metadata keys (both maps) must be lowercase and match `[a-z0-9._-]+`; the
`grpc-` prefix is reserved by the gRPC runtime and `-bin` (binary metadata)
keys are rejected.

---

## Security / Certificates

### `ssl` -- SSL/TLS certificate validation

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Target hostname |
| `port` | int | O | 443 | Target port. Validation: 1-65535 |
| `criticalDays` | int | O | 30 | Days before expiration to mark **Down** (paging). Must be `0..3650` |
| `warningDays` | int | O | 30 | Days before expiration to mark **Warning** (amber, non-paging). Must be `>= criticalDays`, `0..3650` |
| `thresholdDays` | int | O | 30 | **Legacy alias** for `criticalDays`. Read for back-compat; `criticalDays` wins when both are set |
| `timeout` | duration | O | 10s | Connection + handshake timeout (max: 60s) |
| `server_name` | string | O | same as host | Override SNI server name |

Min period: 1 hour. Default period: 6 hours.

**Graduated expiry & whole-chain reporting.** Expiry is evaluated against the
**minimum days-remaining across the whole presented certificate chain** (leaf +
intermediates), not just the leaf — an intermediate that expires first now drives
the status. Two tiers:

- `min <= criticalDays` → **Down** (`severity: "critical"`), pages, as before.
- `criticalDays < min <= warningDays` → **Warning** (`severity: "warning"`,
  `certExpiryWarning: true`) — amber, counts as up, opens no incident; rollups
  show **Degraded**.
- else → **Up**.

Defaults keep `warningDays == criticalDays == 30`, so behaviour is **unchanged**
until you widen `warningDays`. A handshake failure / actual expiry is **Down** as
before (trust is enforced at handshake). Recommended: lower `criticalDays` (e.g.
to 7) and widen `warningDays` (e.g. 30) so a valid-but-soon-expiring cert is a
warning, not a month-long fake outage.

Output (in `result.output`):

| Key | Description |
|-----|-------------|
| `chain` | Array of `{subject, issuer, notAfter, daysRemaining}`, one per presented cert |
| `soonestExpiring` | `{subject, daysRemaining}` for the cert driving the status |
| `chainLength` | Number of presented certificates |
| `chainVerified` | `true` when the handshake produced a verified chain |
| `certExpiryWarning` | `true` only in the warning tier |
| `severity` | `"critical"`, `"warning"`, or `""` |
| `subject`, `issuer`, `not_after`, `days_remaining`, `serial_number`, `dns_names` | Leaf-certificate fields (kept for back-compat) |

`metrics.days_remaining` is the **chain minimum** (the meaningful number to graph).

---

### `domain` -- Domain name expiration

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `domain` | string | R | | Domain name to check (e.g., `"google.com"`) |
| `threshold_days` | int | O | 30 | Days before expiration to consider failed |

Min period: 6 hours. Default period: 24 hours.

---

## Email

### `smtp` -- SMTP server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | SMTP server hostname |
| `port` | int | O | 25 | Port. Validation: 1-65535 |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `starttls` | bool | O | false | Use STARTTLS. Cannot combine with port 465 (implicit TLS) |
| `tls_verify` | bool | O | false | Verify TLS certificate |
| `tls_server_name` | string | O | | Override TLS server name |
| `ehlo_domain` | string | O | `solidping.local` | EHLO domain string |
| `expect_greeting` | string | O | | Expected greeting string |
| `check_auth` | bool | O | false | Test authentication |
| `username` | string | O | | Auth username |
| `password` | string | O | | Auth password |

---

### `pop3` -- POP3 server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | POP3 server hostname |
| `port` | int | O | 110 | Port. Validation: 1-65535 |
| `tls` | bool | O | false | Use implicit TLS. Cannot combine with `starttls` |
| `starttls` | bool | O | false | Use STARTTLS. Cannot combine with `tls` |
| `tls_verify` | bool | O | false | Verify TLS certificate |
| `tls_server_name` | string | O | | Override TLS server name |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `expect_greeting` | string | O | | Expected greeting string |
| `username` | string | O | | Auth username |
| `password` | string | O | | Auth password |

---

### `imap` -- IMAP server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | IMAP server hostname |
| `port` | int | O | 143 | Port. Validation: 1-65535 |
| `tls` | bool | O | false | Use implicit TLS. Cannot combine with `starttls` |
| `starttls` | bool | O | false | Use STARTTLS. Cannot combine with `tls` |
| `tls_verify` | bool | O | false | Verify TLS certificate |
| `tls_server_name` | string | O | | Override TLS server name |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `expect_greeting` | string | O | | Expected greeting string |
| `username` | string | O | | Auth username |
| `password` | string | O | | Auth password |

---

## Database

### `postgresql` -- PostgreSQL health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Database hostname |
| `port` | int | O | 5432 | Port. Validation: 1-65535 |
| `username` | string | R | | Database username |
| `password` | string | O | | Database password |
| `database` | string | O | `postgres` | Database name |
| `ssl_mode` | string | O | `prefer` | SSL mode: `disable`, `require`, `verify-ca`, `verify-full`, `prefer` |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `query` | string | O | `SELECT 1` | Health check query. Must start with SELECT |

---

### `mysql` -- MySQL/MariaDB health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Database hostname |
| `port` | int | O | 3306 | Port. Validation: 1-65535 |
| `username` | string | R | | Database username |
| `password` | string | O | | Database password |
| `database` | string | O | | Database name |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `query` | string | O | `SELECT 1` | Health check query. Must start with SELECT |

---

### `mongodb` -- MongoDB health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Database hostname |
| `port` | int | O | 27017 | Port. Validation: 1-65535 |
| `username` | string | O | | Auth username |
| `password` | string | O | | Auth password |
| `database` | string | O | | Database name |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |

---

### `mssql` -- Microsoft SQL Server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Database hostname |
| `port` | int | O | 1433 | Port. Validation: 1-65535 |
| `username` | string | R | | Database username |
| `password` | string | O | | Database password |
| `database` | string | O | `master` | Database name |
| `encrypt` | string | O | `false` | Encryption mode: `true`, `false`, `disable` |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `query` | string | O | `SELECT 1` | Health check query. Must start with SELECT |

---

### `oracle` -- Oracle Database health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Database hostname |
| `port` | int | O | 1521 | Port. Validation: 1-65535 |
| `username` | string | R | | Database username |
| `password` | string | O | | Database password |
| `serviceName` | string | O | `ORCL` | Oracle service name. Cannot combine with `sid` |
| `sid` | string | O | | Oracle SID. Cannot combine with `serviceName` |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `query` | string | O | `SELECT 1 FROM DUAL` | Health check query. Must start with SELECT |

---

### `clickhouse` -- ClickHouse health (native protocol)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Database hostname |
| `port` | int | O | 9000 (9440 when `secure`) | Native-protocol port. Validation: 1-65535 |
| `username` | string | O | `default` | Database username |
| `password` | string | O | | Database password |
| `database` | string | O | `default` | Database name |
| `secure` | bool | O | false | Native protocol over TLS (required by ClickHouse Cloud) |
| `tls_verify` | bool | O | false | Verify the server certificate. Requires `secure` |
| `timeout` | duration | O | 10s | Connection timeout (max: 30s) |
| `query` | string | O | `SELECT 1` | Health check query. Must start with SELECT |

Uses the native (binary) protocol via `clickhouse-go`, not the HTTP interface.
The query result is rendered from the column's own scan type (ClickHouse is
strictly typed — `SELECT 1` is a UInt8, not text). Output includes
`server_version`; metrics are `connection_time_ms`, `query_time_ms`,
`total_time_ms`.

---

### `redis` -- Redis health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Redis hostname |
| `port` | int | O | 6379 | Port. Validation: 1-65535 |
| `password` | string | O | | Auth password |
| `database` | int | O | 0 | Database index. Validation: 0-15 |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |

---

## Message Queue

### `kafka` -- Kafka cluster health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `brokers` | string[] or string | R | | Broker addresses (`["host:port"]` or `"host:port,host2:port"`). Each must include port |
| `topic` | string | O | | Kafka topic. Required when `produceTest` is true |
| `saslMechanism` | string | O | | SASL auth mechanism: `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `saslUsername` | string | O | | SASL username (required when `saslMechanism` is set) |
| `saslPassword` | string | O | | SASL password (required when `saslMechanism` is set) |
| `tls` | bool | O | false | Use TLS |
| `tlsSkipVerify` | bool | O | false | Skip TLS certificate verification |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `produceTest` | bool | O | false | Test by producing a message (requires `topic`) |

---

### `rabbitmq` -- RabbitMQ health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | RabbitMQ hostname |
| `port` | int | O | 5672 | AMQP port. Validation: 1-65535 |
| `username` | string | R | | Auth username |
| `password` | string | O | | Auth password |
| `vhost` | string | O | `/` | Virtual host |
| `tls` | bool | O | false | Use TLS (amqps://) |
| `mode` | string | O | `amqp` | Check mode: `amqp` or `management` |
| `managementPort` | int | O | 15672 | Management API port (for `management` mode). Validation: 1-65535 |
| `queue` | string | O | | Queue name to verify |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |

---

### `mqtt` -- MQTT broker health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | MQTT broker hostname |
| `port` | int | O | 1883 (8883 with TLS) | Port. Validation: 1-65535 |
| `username` | string | O | | Auth username |
| `password` | string | O | | Auth password |
| `topic` | string | O | `solidping/healthcheck` | Topic for publish test. Must not contain wildcards (`#` or `+`) |
| `tls` | bool | O | false | Use TLS |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |

---

## Remote Access

### `ssh` -- SSH server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | SSH server hostname |
| `port` | int | O | 22 | Port. Validation: 1-65535 |
| `timeout` | duration | O | | Connection timeout (max: 60s) |
| `expected_fingerprint` | string | O | | Expected server key fingerprint |
| `username` | string | O | | Auth username. Requires `password` or `private_key` |
| `password` | string | O | | Auth password. Cannot combine with `private_key` |
| `private_key` | string | O | | PEM-encoded private key. Cannot combine with `password` |
| `command` | string | O | | Command to execute (requires `username`) |
| `expected_exit_code` | int | O | 0 | Expected exit code from command |
| `expected_output` | string | O | | String expected in command output (requires `command`) |
| `expected_output_pattern` | string | O | | Regex pattern expected in output (requires `command`) |

---

### `ftp` -- FTP server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | FTP server hostname |
| `port` | int | O | 21 | Port. Validation: 1-65535 |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `username` | string | O | `anonymous` | Auth username |
| `password` | string | O | | Auth password |
| `tls_mode` | string | O | `none` | TLS mode: `none`, `explicit`, `implicit` |
| `tls_verify` | bool | O | false | Verify TLS certificate |
| `passive_mode` | bool | O | false | Use passive mode |
| `path` | string | O | | Directory or file path to verify |

---

### `sftp` -- SFTP server health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | SFTP server hostname |
| `port` | int | O | 22 | Port. Validation: 1-65535 |
| `timeout` | duration | O | 10s | Connection timeout (max: 60s) |
| `username` | string | R | | Auth username |
| `password` | string | O | | Auth password. Cannot combine with `private_key`. One of `password`/`private_key` required |
| `private_key` | string | O | | PEM-encoded private key. Cannot combine with `password` |
| `path` | string | O | | Directory or file path to verify |

---

## Infrastructure

### `snmp` -- SNMP device monitoring

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Target hostname |
| `port` | int | O | 161 | Port. Validation: 1-65535 |
| `version` | string | O | `2c` | SNMP version: `1`, `2c`, `3` |
| `community` | string | O | `public` | Community string (v1/v2c) |
| `oid` | string | R | | Object identifier to query |
| `expectedValue` | string | O | | Expected value from OID query |
| `operator` | string | O | `equals` | Comparison operator: `equals`, `contains`, `greater_than`, `less_than`, `not_equals` |
| `username` | string | O | | SNMPv3 username (required for v3) |
| `authProtocol` | string | O | | Auth protocol: `MD5`, `SHA`, `SHA-256`, `SHA-512` |
| `authPassword` | string | O | | Auth password |
| `privProtocol` | string | O | | Privacy protocol: `DES`, `AES`, `AES-192`, `AES-256`. Requires `authProtocol` |
| `privPassword` | string | O | | Privacy password |
| `timeout` | duration | O | 10s | Query timeout (max: 60s) |

---

### `docker` -- Docker container health

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | O | `unix:///var/run/docker.sock` | Docker daemon host (must start with `unix://` or `tcp://`) |
| `containerName` | string | O* | | Container name. *At least one of `containerName`/`containerId` required |
| `containerId` | string | O* | | Container ID. *At least one of `containerName`/`containerId` required |
| `timeout` | duration | O | 10s | Check timeout (max: 60s) |
| `restartLoopMinRestarts` | int | O | `0` (disabled) | Opt-in restart-loop detection. When `> 0`, a *running* container whose lifetime restart count is at least this many **and** that (re)started within `restartLoopWindow` is flagged as a suspected crash-loop |
| `restartLoopWindow` | duration | O | 120s (when enabled) | Recency window for restart-loop detection. Only applied when `restartLoopMinRestarts > 0`. Max: 24h |

Requires Docker socket access.

**Restart-loop detection (opt-in heuristic).** A container that is *running* at
inspect time but crash-looping (restarting every few seconds) would otherwise
report `up`. When `restartLoopMinRestarts > 0`, the checker flags it: an actively
looping container always shows a freshly-recent `startedAt`, so the recency
window (`restartLoopWindow`) carries the signal while the count floor
(`restartLoopMinRestarts`) keeps a single deploy restart from tripping it. A
detected loop returns the **`warning`** status (counts as up, rolls up to
`degraded`, does not page); a genuinely not-running/unhealthy container still
returns `down` and pages. Not-running and unhealthy short-circuits are evaluated
*before* the loop branch. Additional output keys when detection is enabled:

| Output key | Type | Meaning |
|------------|------|---------|
| `secondsSinceStart` | number | Seconds since the current run started (always emitted when detection is enabled, even below threshold) |
| `restartLoop` | bool | `true` only when a loop is suspected (status `warning`) |

`metrics.restartCount` is always emitted (lifetime restart count), independent of
detection.

---

## Specialized

### `heartbeat` -- Passive heartbeat monitoring

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `token` | string | O | | Token to validate incoming heartbeat pings |
| `require_hmac` | bool | O | `false` | Refuse unsigned (`SP1`) beats on the embedded TCP/UDP push transports |

The heartbeat checker is passive: it waits for external systems to ping it. The check's `period` and `grace_period` on the check object (not in config) determine when the check is considered down.

`require_hmac` only affects the embedded push transports (spec 2026-09-01-06). With
`false` the check accepts `SP1` (plaintext token), `SP2` (HMAC-signed) and the HTTPS
ingest; with `true` only `SP2` and HTTPS are accepted and an `SP1` beat is dropped
silently on UDP. A mistyped value (`"true"`, `1`, …) is a validation error rather than
a fallback to `false` — silently downgrading a security setting is not an acceptable
failure mode. **Turning it on should be paired with a token rotation**: a check that
ever accepted `SP1` has leaked its token to any passive sniffer, and that token is
also the `SP2` signing key.

---

### `js` -- Custom JavaScript scripts

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `script` | string | R | | JavaScript source code. Max: 64KB. Validated for syntax errors |
| `timeout` | duration | O | 30s | Execution timeout (max: 60s) |
| `env` | map[string]string | O | | Environment variables available to the script. Max: 50 entries |

Requires scripting runtime. Script runs in a sandboxed Goja (Go-based JS) environment.

---

### `browser` -- Headless Chrome monitoring

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `url` | string | R | | Page URL (must start with `http://` or `https://`). `file://`, `data:`, `javascript:` schemes are rejected |
| `waitSelector` | string | O | | CSS selector to wait for before checking |
| `keyword` | string | O | | Keyword to search in page content |
| `invertKeyword` | bool | O | false | Fail if keyword IS found |
| `timeout` | duration | O | 30s | Page load timeout (max: 120s) |
| `screenshot` | bool | O | false | Capture a PNG of the page when the check FAILS, kept on the incident this failure opens or reopens |

Requires Chrome/Chromium installed.

---

### `a2s` -- Source engine A2S query

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Game server hostname |
| `port` | int | O | 27015 | Query port. Validation: 1-65535 |
| `timeout` | duration | O | 10s | Query timeout (max: 30s) |
| `minPlayers` | int | O | | Minimum player count to consider healthy. Must be >= 0 |
| `maxPlayers` | int | O | | Maximum player count to consider healthy. Must be >= 0 |

Uses the Source Engine A2S_INFO query protocol — covers Counter-Strike, TF2, Garry's Mod, Rust, ARK, etc.

---

### `minecraft` -- Minecraft Server List Ping

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | R | | Minecraft server hostname |
| `port` | int | O | 25565 (java) / 19132 (bedrock) | Query port. Validation: 0-65535 |
| `edition` | string | O | `java` | `java` or `bedrock` |
| `timeout` | duration | O | 10s | Query timeout (max: 30s) |
| `minPlayers` | int | O | | Minimum player count to consider healthy. Must be >= 0 |
| `maxPlayers` | int | O | | Maximum player count to consider healthy. Must be >= 0 |

Uses Server List Ping (Java 1.7+ status protocol over TCP) or RakNet Unconnected Ping (Bedrock UDP).
