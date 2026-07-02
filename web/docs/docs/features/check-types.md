---
sidebar_position: 1
title: Check Types
---

# Check Types

SolidPing supports **36 check types** across multiple categories for monitoring your services. Each check type has specific configuration options and validation capabilities.

## Network Checks

### HTTP/HTTPS

Monitor web services, APIs, and websites.

**URL Format:**
```
http://example.com/path
https://api.example.com/health
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | The endpoint to check | `https://api.example.com/health` |
| Method | HTTP method | `GET`, `POST`, `PUT`, `DELETE` |
| Timeout | Request timeout | `30s` |
| Expected Status | Status code to expect | `200`, `2XX` (wildcard) |
| Headers | Custom request headers | `Authorization: Bearer token` |
| Body | Request body (for POST/PUT) | `{"key": "value"}` |
| Body Match | Pattern to match in response | `"status": "ok"` |
| Basic Auth | Username and password | `user:password` |
| Custom User-Agent | Override the default user-agent | `SolidPing/1.0` |

**Status Code Matching:**
- Exact match: `200`, `201`, `404`
- Wildcard: `2XX` (any 2xx status), `5XX` (any 5xx status)

**Examples:**

```yaml
# Basic health check
url: https://api.example.com/health
method: GET
expected_status: 200
timeout: 10s

# API with authentication
url: https://api.example.com/v1/users
method: GET
headers:
  Authorization: Bearer your-token
  Accept: application/json
expected_status: 200
body_match: '"users":'

# POST request
url: https://api.example.com/webhook
method: POST
headers:
  Content-Type: application/json
body: '{"test": true}'
expected_status: 200
```

### TCP

Monitor TCP services like databases, message queues, and custom services.

**URL Format:**
```
tcp://hostname:port
tcps://hostname:port  # With TLS
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | Target hostname | `db.example.com` |
| Port | Target port | `5432` |
| TLS | Enable TLS/SSL | `true` / `false` |
| Timeout | Connection timeout | `10s` |

### UDP

Check UDP port reachability.

**URL Format:**
```
udp://hostname:port
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | Target hostname | `dns.example.com` |
| Port | Target port | `53` |
| Timeout | Connection timeout | `10s` |

### ICMP (Ping)

Check host availability using ICMP echo requests.

**URL Format:**
```
ping://hostname
icmp://hostname
```

| Option | Description | Default |
|--------|-------------|---------|
| Host | Target hostname or IP | - |
| Count | Number of packets | `3` |
| Interval | Time between packets | `1s` |
| Timeout | Total timeout | `10s` |

:::note Permissions
ICMP checks may require elevated permissions on some systems. Docker containers typically need `NET_RAW` capability.
:::

### DNS

Verify DNS resolution and record values.

**URL Format:**
```
dns://resolver/domain?type=A
dns://8.8.8.8/example.com?type=MX
```

| Option | Description | Example |
|--------|-------------|---------|
| Resolver | DNS server to query | `8.8.8.8`, `1.1.1.1` |
| Domain | Domain to resolve | `example.com` |
| Type | Record type | `A`, `AAAA`, `MX`, `TXT`, `CNAME`, `NS`, `SOA` |
| Expected | Expected values | `93.184.216.34` |

### WebSocket

Monitor WebSocket endpoint availability.

**URL Format:**
```
ws://hostname/path
wss://hostname/path  # With TLS
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | WebSocket endpoint | `wss://api.example.com/ws` |
| Timeout | Connection timeout | `10s` |

## Security & Certificates

### SSL/TLS Certificate

Monitor SSL/TLS certificate expiration and validity.

| Option | Description | Example |
|--------|-------------|---------|
| Host | Target hostname | `example.com` |
| Port | HTTPS port | `443` |
| Warning Days | Days before expiry to warn | `30` |
| Critical Days | Days before expiry to alert | `7` |

**What gets checked:**
- Certificate validity
- Expiration date
- Chain validation
- Hostname verification

### Domain Expiration

Monitor domain name registration expiration via WHOIS lookup.

| Option | Description | Example |
|--------|-------------|---------|
| Domain | Domain name to check | `example.com` |
| Warning Days | Days before expiry to warn | `30` |
| Critical Days | Days before expiry to alert | `7` |

### DNSBL (DNS Blocklist)

Check whether an IP address or hostname is listed on DNS-based blocklists (DNSBLs) such as Spamhaus, SpamCop, Barracuda, or UCEPROTECT. This is essential for monitoring the reputation of mail servers and public IPs.

| Option | Description | Default |
|--------|-------------|---------|
| Target | IPv4 address or hostname to check | - (required) |
| Blocklists | List of DNSBL zones to query | `zen.spamhaus.org`, `bl.spamcop.net`, `b.barracudacentral.org`, `dnsbl-1.uceprotect.net` |
| Nameserver | Custom DNS resolver (`host:port`) | system resolver |
| Timeout | Query timeout (max `60s`) | `10s` |

Hostnames are resolved to IPv4 before lookup. The check **fails** when the target is listed on at least one blocklist, and **succeeds** when it is clean. If every queried zone errors out, the result is inconclusive (timeout).

## Database Checks

### PostgreSQL

Monitor PostgreSQL database connectivity and query execution.

**URL Format:**
```
postgres://user:password@hostname:5432/database
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | Connection string | `postgres://user:pass@db:5432/mydb` |
| Query | Optional test query | `SELECT 1` |
| Timeout | Connection timeout | `10s` |

### MySQL / MariaDB

Monitor MySQL or MariaDB database connectivity and query execution.

**URL Format:**
```
mysql://user:password@hostname:3306/database
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | Connection string | `mysql://user:pass@db:3306/mydb` |
| Query | Optional test query | `SELECT 1` |
| Timeout | Connection timeout | `10s` |

### MongoDB

Monitor MongoDB connectivity using the ping command.

**URL Format:**
```
mongodb://user:password@hostname:27017/database
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | Connection string | `mongodb://user:pass@db:27017/mydb` |
| Timeout | Connection timeout | `10s` |

### Redis

Monitor Redis server availability using the PING command.

**URL Format:**
```
redis://hostname:6379
redis://:password@hostname:6379
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | Connection string | `redis://redis:6379` |
| Timeout | Connection timeout | `10s` |

### Microsoft SQL Server

Monitor MSSQL database connectivity and query execution.

**URL Format:**
```
sqlserver://user:password@hostname:1433?database=mydb
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | Connection string | `sqlserver://sa:pass@db:1433?database=mydb` |
| Query | Optional test query | `SELECT 1` |
| Timeout | Connection timeout | `10s` |

### Oracle Database

Monitor Oracle database connectivity and query execution.

**URL Format:**
```
oracle://user:password@hostname:1521/service
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | Connection string | `oracle://user:pass@db:1521/orcl` |
| Query | Optional test query | `SELECT 1 FROM DUAL` |
| Timeout | Connection timeout | `10s` |

## Email Services

### SMTP

Monitor SMTP server availability with optional authentication.

**URL Format:**
```
smtp://hostname:25
smtp://hostname:587
smtps://hostname:465  # With SSL
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | SMTP server | `smtp.example.com` |
| Port | SMTP port | `25`, `587`, `465` |
| STARTTLS | Enable STARTTLS | `true` / `false` |
| Auth | Test authentication | `user:password` |
| Timeout | Connection timeout | `10s` |

### IMAP

Monitor IMAP server availability and authentication.

**URL Format:**
```
imap://hostname:143
imaps://hostname:993  # With SSL
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | IMAP server | `imap.example.com` |
| Port | IMAP port | `143`, `993` |
| Timeout | Connection timeout | `10s` |

### POP3

Monitor POP3 server availability and authentication.

**URL Format:**
```
pop3://hostname:110
pop3s://hostname:995  # With SSL
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | POP3 server | `pop3.example.com` |
| Port | POP3 port | `110`, `995` |
| Timeout | Connection timeout | `10s` |

### Email Reception (Passive Inbox)

Verify end-to-end email **delivery** rather than just server connectivity. SolidPing generates a unique address for the check; when a message arrives at that address, the check is marked up. This is ideal for monitoring a full sending pipeline (queue → relay → inbox).

| Option | Description | Default |
|--------|-------------|---------|
| Token | Secret part of the unique receiving address | auto-generated |

The receiving domain is configured by your administrator. Point a periodic test email (or your application's "send a heartbeat" job) at the generated address, and SolidPing reports an incident if expected mail stops arriving.

:::note Passive check
Email-reception checks are receive-only — SolidPing waits for mail instead of actively probing a server. Combine it with an [SMTP check](#smtp) to also monitor outbound connectivity.
:::

## Remote Access

### SSH

Monitor SSH server availability.

**URL Format:**
```
ssh://hostname:22
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | SSH server | `server.example.com` |
| Port | SSH port | `22` |
| Timeout | Connection timeout | `10s` |

### FTP

Monitor FTP server availability.

**URL Format:**
```
ftp://hostname:21
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | FTP server | `ftp.example.com` |
| Port | FTP port | `21` |
| Timeout | Connection timeout | `10s` |

### SFTP

Monitor SFTP server availability.

**URL Format:**
```
sftp://hostname:22
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | SFTP server | `sftp.example.com` |
| Port | SFTP port | `22` |
| Timeout | Connection timeout | `10s` |

## Messaging & Streaming

### gRPC

Monitor gRPC services using the standard health check protocol (`grpc.health.v1`).

**URL Format:**
```
grpc://hostname:50051
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | gRPC server | `api.example.com` |
| Port | gRPC port | `50051` |
| Service | Service name to check | `my.service.v1` |
| TLS | Enable TLS | `true` / `false` |
| Timeout | Connection timeout | `10s` |

### Kafka

Monitor Apache Kafka broker connectivity.

**URL Format:**
```
kafka://hostname:9092
```

| Option | Description | Example |
|--------|-------------|---------|
| Broker | Kafka broker address | `kafka:9092` |
| Timeout | Connection timeout | `10s` |

### RabbitMQ

Monitor RabbitMQ message queue connectivity.

**URL Format:**
```
amqp://user:password@hostname:5672
```

| Option | Description | Example |
|--------|-------------|---------|
| URL | AMQP connection string | `amqp://guest:guest@rabbitmq:5672` |
| Timeout | Connection timeout | `10s` |

### MQTT

Monitor MQTT broker connectivity via subscription.

**URL Format:**
```
mqtt://hostname:1883
mqtts://hostname:8883  # With TLS
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | MQTT broker | `mqtt.example.com` |
| Port | MQTT port | `1883` |
| Timeout | Connection timeout | `10s` |

## Telephony

### SIP (VoIP)

Monitor SIP servers used for voice/VoIP, either by checking reachability with an `OPTIONS` ping or by verifying that a user can register (`REGISTER` with digest authentication).

**Address Format:**
```
host:port
udp://host:5060
tcp://host:5060
tls://host:5061
```

| Option | Description | Default |
|--------|-------------|---------|
| Host | SIP server hostname or IP | - (required) |
| Port | SIP port | `5060` (UDP/TCP), `5061` (TLS) |
| Transport | `udp`, `tcp`, or `tls` | `udp` |
| Mode | `options` (ping) or `register` (auth) | `options` |
| Domain | SIP domain for From/To headers | same as Host |
| Username | SIP username (required for register) | - |
| Password | SIP password for digest auth (register) | - |
| Expect Status | Accepted SIP status codes for OPTIONS (e.g. `200,405`) | - |
| TLS Verify | Verify the server certificate (TLS transport) | `false` |
| Timeout | Request timeout (max `60s`) | `5s` |

- **OPTIONS mode** succeeds when the server returns a valid SIP status code (matching `expect_status` if set).
- **REGISTER mode** performs the standard two-step challenge/response and succeeds only on a final `200 OK`.

### NTP (Time Server)

Monitor an NTP time server. Unlike a plain UDP/123 reachability probe, this checker sends a real NTP request and judges the server **as a clock**: it confirms the server returns a valid response and reports itself healthy (stratum, leap indicator, and root distance, via the server's own self-report — no trust in the worker's clock). Two optional, opt-in thresholds let you also alert on the measured clock offset and on the server's stratum depth.

| Option | Description | Default |
|--------|-------------|---------|
| Host | NTP server hostname or IP | - (required) |
| Port | NTP UDP port | `123` |
| Version | NTP protocol version (`3` or `4`) | `4` |
| Timeout | Query timeout (max `60s`) | `5s` |
| Offset warn (ms) | Mark **warning** when the absolute clock offset exceeds this. `0` = off | off |
| Offset critical (ms) | Mark **down** when the absolute clock offset exceeds this. Must be ≥ Offset warn. `0` = off | off |
| Max stratum | Mark **down** when the server's stratum exceeds this (`1`–`15`). `0` = off | off |

- **Default verdict** = reachable **and** the server reports a usable clock. A Kiss-o'-Death (stratum 0), an unsynchronized server (stratum 16), a `LeapNotInSync` leap indicator, or an out-of-range root distance all yield **down**.
- **Clock offset is measured relative to the worker's own clock.** A worker whose clock is itself skewed will report a misleading offset, so the offset thresholds are opt-in rather than the default verdict — keep this in mind when running across a distributed worker fleet.
- Metrics exposed: clock offset, RTT, stratum, root delay, root dispersion, root distance, poll interval, and precision.

:::note Egress
NTP uses outbound **UDP port 123**, which is frequently blocked by egress firewalls. A blocked path surfaces deterministically as `down`/`timeout`.
:::

## Infrastructure

### SNMP

Monitor devices via SNMP protocol.

| Option | Description | Example |
|--------|-------------|---------|
| Host | Target device | `switch.example.com` |
| Community | SNMP community string | `public` |
| OID | Object identifier | `1.3.6.1.2.1.1.1.0` |
| Version | SNMP version | `2c` |
| Timeout | Connection timeout | `10s` |

### Docker

Monitor remote Docker daemon connectivity.

**URL Format:**
```
docker://hostname:2375
```

| Option | Description | Example |
|--------|-------------|---------|
| Host | Docker daemon | `docker.example.com` |
| Port | Docker API port | `2375`, `2376` (TLS) |
| TLS | Enable TLS | `true` / `false` |
| Timeout | Connection timeout | `10s` |

### A2S Game Server (Source / Steam)

Query Source-engine and Steam game servers using the Valve A2S protocol (`A2S_INFO`).

| Option | Description | Example |
|--------|-------------|---------|
| Host | Game server address | `game.example.com` |
| Port | Query port | `27015` |
| Timeout | Connection timeout | `10s` |

### Minecraft

Monitor Minecraft servers (both Java and Bedrock editions), with optional player-count thresholds.

| Option | Description | Default |
|--------|-------------|---------|
| Host | Server hostname or IP | - (required) |
| Port | Server port | `25565` (Java), `19132` (Bedrock) |
| Edition | `java` or `bedrock` | `java` |
| Min Players | Alert if fewer players are online (0 = off) | `0` |
| Max Players | Alert if more players are online (0 = off) | `0` |
| Timeout | Query timeout (max `30s`) | `10s` |

The check reports online players, max players, MOTD, and version. It fails if the query fails or the player count falls outside the configured bounds.

### Freebox Line (xDSL / FTTH)

Monitor the quality of a Freebox broadband line (xDSL or FTTH) through the Freebox OS API. The check connects via a stored **Freebox integration connection** rather than a direct address.

| Option | Description | Default |
|--------|-------------|---------|
| Connection | Reference to a `freebox` integration connection | - (required) |
| Link Type | `xdsl` or `ftth` | - (required) |
| Min Sync Rate (down) | Minimum downstream sync rate, kbps (xDSL) | `0` (off) |
| Min SNR Margin (down) | Minimum downstream SNR margin, dB (xDSL) | `0` (off) |
| Max Attenuation | Maximum downstream attenuation, dB (xDSL) | `0` (off) |
| Max CRC Errors | Maximum CRC errors per run (xDSL) | `0` (off) |
| Min / Max RX Power | Optical receive power bounds, mW (FTTH) | `0` (off) |

The check reports sync rates, SNR, attenuation, CRC counts (xDSL) or optical power and SFP details (FTTH), and fails when the WAN is down, the link is not trained, or any configured threshold is violated.

### Kubernetes (Workload Replica Health)

Monitor a Kubernetes workload's replica health — the structural analog of how the Docker check mirrors a container's HEALTHCHECK. The check connects via a stored **Kubernetes cluster connection** (an integration of type `kubernetes`) referenced by UID, never an inline credential.

| Option | Description | Default |
|--------|-------------|---------|
| Cluster | Reference to a `kubernetes` integration connection | - (required) |
| Namespace | Workload namespace | - (required) |
| Kind | `Deployment` or `ReplicaSet` | - (required) |
| Name | Workload name | - (required) |
| Timeout | Per-execution API timeout (max 60s) | `10s` |

**Status semantics** (ready vs. desired replicas):

- **Up** — `readyReplicas == desiredReplicas` and `desiredReplicas > 0`.
- **Warning** — `0 < readyReplicas < desiredReplicas` (mid-rollout or partially degraded), or `desiredReplicas == 0` (intentionally scaled to zero — surfaced, not paged).
- **Down** — `readyReplicas == 0` with `desiredReplicas > 0`, a stuck rollout (Deployment `Progressing=False` / `ProgressDeadlineExceeded`), or the workload no longer exists.

Outputs include the namespace, kind, name, container images, and workload conditions; metrics include `desiredReplicas`, `readyReplicas`, `availableReplicas`, `updatedReplicas`, and `unavailableReplicas`.

#### Cluster connection

Register a cluster once under **Integrations → Kubernetes** (it is a data source, not a notification channel). Three authentication modes:

- **API server + token** — an API server URL plus a bearer token (typically a service-account token), optionally a CA certificate (or skip TLS verification).
- **Kubeconfig** — paste a full kubeconfig that resolves to an API server and credentials.
- **In-cluster** — when SolidPing itself runs as a pod in the target cluster, it uses the mounted service-account token; no credentials are stored.

The token / kubeconfig is stored **encrypted** (AES-256-GCM) in the connection's private settings and is never returned to the dashboard. Use **Test connection** to confirm the credentials work — it calls the cluster's `/version` endpoint.

#### Required RBAC

The connection only needs read access to the monitored workloads. Bind a read-only `ClusterRole` to the service account whose token you register:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: solidping-readonly
rules:
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: solidping-readonly
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: solidping-readonly
subjects:
  - kind: ServiceAccount
    name: solidping
    namespace: solidping
```

> Kubernetes discovery (enumerating workloads automatically) builds on this same connection and additionally needs `get`/`list` on `services`, `endpoints`, and `ingresses` (and `nodes`); grant those too if you plan to use it.

## Special Check Types

### Heartbeat

Passive monitoring that expects incoming pings at regular intervals. Instead of SolidPing checking a service, the service pings SolidPing to report it's alive.

| Option | Description | Default |
|--------|-------------|---------|
| Period | Expected ping interval | `60s` |
| Grace | Grace period before incident | `30s` |

**Use cases:**
- Cron job monitoring
- Backup completion verification
- Batch process health checks
- IoT device connectivity

### JavaScript

Custom monitoring scripts with arbitrary logic. Write JavaScript code that runs on each check cycle.

Minimum period: `30s` (default `1m`) — see [Check Intervals](#check-intervals).

**Use cases:**
- Complex multi-step API workflows
- Custom business logic validation
- Conditional checks based on time or state
- Aggregating multiple checks into one

### Browser

Headless browser-based monitoring using a real browser engine.

Minimum period: `60s` (default `5m`) — a headless-browser run costs several
seconds, so faster periods would occupy a monitoring slot continuously. See
[Check Intervals](#check-intervals).

**Use cases:**
- Single-page application monitoring
- Login flow verification
- Visual regression detection
- JavaScript-rendered content checks

## Common Options

All check types support these common options:

| Option | Description | Default |
|--------|-------------|---------|
| `name` | Display name | - |
| `description` | Description | - |
| `enabled` | Enable/disable check | `true` |
| `period` | Check interval | `60s` |
| `timeout` | Request timeout | `30s` |
| `regions` | Worker regions to run from | all |
| `incident_threshold` | Failures before incident | `1` |
| `escalation_threshold` | Failures before escalation | `3` |
| `recovery_threshold` | Successes before recovery | `1` |

## Check Intervals

Supported interval formats:

- Seconds: `10s`, `30s`, `60s`
- Minutes: `1m`, `5m`, `15m`
- Hours: `1h`, `6h`, `24h`

### Minimum intervals

The API enforces a minimum period per check type — both in the dashboard and
on direct API calls (`400 VALIDATION_ERROR` naming the floor):

| Check type | Minimum period | Default period |
|------------|----------------|----------------|
| `browser` | `60s` | `5m` |
| `js` | `30s` | `1m` |
| `ssl` | `1h` | `6h` |
| `domain` | `6h` | `24h` |
| `dnsbl` | `15m` | `1h` |
| All other types | `10s` | `1m` |

Heavy check types (headless browser, custom scripts) carry higher floors
because each run can occupy a monitoring slot for several seconds — a fast
period would keep a runner busy full-time. Existing checks are grandfathered:
a period created before a floor was raised keeps working, and the limit
applies on the next edit. The check detail page shows a warning when a check
occupies 50% or more of a runner slot (its *duty cycle*).

Recommended minimum: `30s` for production.

## Best Practices

1. **Use appropriate timeouts** - Set timeouts based on expected response times
2. **Avoid excessive frequency** - Sub-minute checks increase load on both SolidPing and targets
3. **Use meaningful names** - Make check names descriptive for quick identification
4. **Set appropriate thresholds** - Balance between noise and missing real issues
5. **Monitor from multiple regions** - Use distributed workers for global services
6. **Use database checks for actual connectivity** - Prefer native database checks (PostgreSQL, MySQL, Redis) over generic TCP checks for database monitoring
7. **Leverage heartbeat checks** - Use heartbeat mode for monitoring cron jobs and batch processes
