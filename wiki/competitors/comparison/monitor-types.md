# Uptime Monitoring Comparison — Monitor Types

| Monitor Type | BetterStack | UptimeRobot | Pingdom | StatusCake | Checkly | Healthchecks.io | Uptime Kuma | Gatus | Pulsetic | SolidPing |
|--------------|-------------|-------------|---------|------------|---------|-----------------|-------------|-------|----------|-----------|
| **HTTP/HTTPS** | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Keyword monitoring** | ✅ | ✅ | ✅ | ✅ | ✅ (assertions) | ❌ | ✅ | ✅ (conditions) | ✅ (string) | ✅ (string + regex) |
| **JSON body validation** | ❌ | ❌ | ❌ | ❌ | ✅ (assertions) | ❌ | ✅ (JSONPath) | ✅ (JSONPath) | ❌ | ✅ (JSONPath) |
| **Ping (ICMP)** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **TCP port** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **UDP port** | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **DNS** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ |
| **SMTP** | ✅ | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ (STARTTLS) | ❌ | ✅ |
| **SSH** | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ |
| **POP3/IMAP** | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ (STARTTLS) | ❌ | ✅ |
| **WebSocket** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ |
| **Heartbeat/Cron** | ✅ | ✅ (Pro) | ❌ | ✅ (all plans) | ✅ | ✅ (core) | ✅ | ❌ | ✅ | ✅ |
| **SSL certificate** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Domain expiration** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ (v2.1) | ❌ | ✅ | ✅ (RDAP, WHOIS fallback) |
| **FTP / SFTP** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **gRPC** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ |
| **Playwright/Browser** | ✅ | ❌ | ✅ (Transaction) | ❌ | ✅ (core) | ❌ | ❌ | ❌ | ❌ (screenshots only) | ✅ (chromedp) |
| **Page speed** | ❌ | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ⚠️ (RUM, not synthetic) | ❌ |
| **Server monitoring** | ❌ | ❌ | ❌ | ✅ (Linux) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Docker container** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ✅ |
| **Database (Postgres/MySQL/MSSQL/Oracle/Mongo/Redis)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ (partial) | ❌ | ❌ | ✅ (6 engines) |
| **Message queues (Kafka/RabbitMQ/MQTT)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **SNMP** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Game server** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ✅ (A2S + Minecraft) |
| **Email inbox (passive, JMAP)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Custom JS check** | ❌ | ❌ | ❌ | ❌ | ✅ (Playwright) | ❌ | ❌ | ❌ | ❌ | ✅ (sandboxed JS) |
| **External script** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ⚠️ (via JS check) |
| **Cron exit codes** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ |
| **Real User Monitoring (RUM)** | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ (CWV + JS errors) | ❌ |
| **Third-party status page (dependency)** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ **4,300+ providers** | ❌ |

**Most Comprehensive**: SolidPing (43 check types — broadest protocol coverage of any tool surveyed)

> **How the SolidPing number is derived (updated 2026-10-15).** Since batch
> 2026-10-12 the count is registry-driven: `checkerdef.AdvertisedCheckTypeCount()`
> counts the registered types minus those flagged `ExcludedFromTally` (`sleep`,
> a testing type, and `private-location`, which is system-created).
> `TestAdvertisedCheckTypeCountInDocs` fails the build when `README.md` or
> `web/docs/` quote another number. Raw constants for a quick check:
>
> ```bash
> grep -rhoE 'CheckType = "[a-z0-9_-]+"' \
>   server/internal/checkers/checkerdef/*.go | wc -l   # -> 45, minus 2 = 43
> ```
>
> The number has been 32, 38, 40 and now 43 within five months. This wiki is
> not covered by the test, so re-derive it at publish time rather than quoting
> this page from memory.

**Best Free**: UptimeRobot (8 types, 50 free monitors) for SaaS; SolidPing for self-hosted (unlimited)

**Most Flexible Conditions**: Gatus (JSONPath, conditions, external scripts), Checkly (full Playwright assertions), SolidPing (sandboxed JS)

**Enterprise Features**: Pingdom (Transaction monitoring, RUM)

**The one column SolidPing loses**: dependency monitoring — [Pulsetic](../pulsetic.md)
subscribes to 4,300+ third-party providers' own status pages and raises an alert when
*they* declare an incident. It is not a probe, which is exactly why no amount of protocol
breadth covers it: it answers "is it us or is it them?" before anyone opens a terminal.
Uptime Kuma has an open request for the same shape of feature
([#7688](https://github.com/louislam/uptime-kuma/issues/7688)).
