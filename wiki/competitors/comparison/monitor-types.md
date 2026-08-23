# Uptime Monitoring Comparison — Monitor Types

| Monitor Type | BetterStack | UptimeRobot | Pingdom | StatusCake | Checkly | Healthchecks.io | Uptime Kuma | Gatus | SolidPing |
|--------------|-------------|-------------|---------|------------|---------|-----------------|-------------|-------|-----------|
| **HTTP/HTTPS** | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ |
| **Keyword monitoring** | ✅ | ✅ | ✅ | ✅ | ✅ (assertions) | ❌ | ✅ | ✅ (conditions) | ✅ (string + regex) |
| **JSON body validation** | ❌ | ❌ | ❌ | ❌ | ✅ (assertions) | ❌ | ✅ (JSONPath) | ✅ (JSONPath) | ✅ (JSONPath) |
| **Ping (ICMP)** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ |
| **TCP port** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ |
| **UDP port** | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **DNS** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ |
| **SMTP** | ✅ | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ (STARTTLS) | ✅ |
| **SSH** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ |
| **POP3/IMAP** | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ (STARTTLS) | ✅ |
| **WebSocket** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ |
| **Heartbeat/Cron** | ✅ | ✅ (Pro) | ❌ | ✅ (all plans) | ✅ | ✅ (core) | ✅ | ❌ | ✅ |
| **SSL certificate** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ |
| **Domain expiration** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ (v2.1) | ❌ | ✅ (RDAP, WHOIS fallback) |
| **FTP / SFTP** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **gRPC** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ |
| **Playwright/Browser** | ✅ | ❌ | ✅ (Transaction) | ❌ | ✅ (core) | ❌ | ❌ | ❌ | ✅ (Rod) |
| **Page speed** | ❌ | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Server monitoring** | ❌ | ❌ | ❌ | ✅ (Linux) | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Docker container** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| **Database (Postgres/MySQL/MSSQL/Oracle/Mongo/Redis)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ (partial) | ❌ | ✅ (6 engines) |
| **Message queues (Kafka/RabbitMQ/MQTT)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **SNMP** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Game server** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ (A2S + Minecraft) |
| **Email inbox (passive, JMAP)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Custom JS check** | ❌ | ❌ | ❌ | ❌ | ✅ (Playwright) | ❌ | ❌ | ❌ | ✅ (sandboxed JS) |
| **External script** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ⚠️ (via JS check) |
| **Cron exit codes** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ |

**Most Comprehensive**: SolidPing (40 check types — broadest protocol coverage of any tool surveyed)

> **How the SolidPing number is derived (updated 2026-08-23).** Count the
> enumerated `CheckType` constants and subtract the synthetic one:
>
> ```bash
> grep -rhoE 'CheckType = "[a-z0-9_-]+"' \
>   server/internal/checkers/checkerdef/*.go | wc -l   # -> 41
> ```
>
> `sleep` is a testing type, explicitly exempted from the customer-facing docs
> (see `docsAnchorExemptTypes` in `server/internal/checkers/registry/docs_anchor_test.go`),
> leaving **40**. This value has been 32, then 38, now 40 within four months, and
> stale copies of it have been caught twice — re-derive it at publish time rather
> than quoting this page from memory.

**Best Free**: UptimeRobot (8 types, 50 free monitors) for SaaS; SolidPing for self-hosted (unlimited)

**Most Flexible Conditions**: Gatus (JSONPath, conditions, external scripts), Checkly (full Playwright assertions), SolidPing (sandboxed JS)

**Enterprise Features**: Pingdom (Transaction monitoring, RUM)
