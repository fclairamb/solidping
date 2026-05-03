# Uptime Monitoring Services Comparison

Comprehensive comparison of uptime monitoring services for the SolidPing project.

## Quick Overview

| Feature | BetterStack Uptime | UptimeRobot | Pingdom | StatusCake | Checkly | Healthchecks.io |
|---------|-------------------|-------------|---------|------------|---------|-----------------|
| **Founded** | 2021 (as Better Uptime) | 2010 | 2007 | 2010 | 2018 | 2015 |
| **Owner** | Independent | Independent | SolarWinds | Accel-KKR | Independent (VC) | Independent |
| **Primary Market** | Modern DevOps teams | Budget-conscious users | Enterprise | Mid-market | Developer teams | Cron/heartbeat |
| **Pricing Model** | Modular/component | Monitor-based | Monitor + feature | Monitor-based | Usage-based (runs) | Check-based |
| **Best Known For** | Incident management | 50 free monitors | RUM + Enterprise | Broad protocol support | Monitoring as code | Cron monitoring |
| **Self-hosted** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ (BSD) |
| **API Version** | v2 (v3 for incidents) | v2 (v3 available) | 3.1 (2.1 legacy) | v1 | v1 | v3 |

> **Also analyzed**: [Site24x7](site24x7.md) (Zoho/ManageEngine) — all-in-one mid-market alternative with 100+ monitor types, 130+ probe locations, 50-monitor free tier, and built-in APM/RUM. Not included in tables below to keep them focused on uptime-first competitors.

> **Where SolidPing stands today (May 2026)**: 32 check types (broadest of any tool surveyed), 9 native notification channels, multi-region distributed workers, status pages with availability, maintenance windows, **adaptive incident resolution + group-incident correlation + ack/snooze/manual-resolve**, **on-call schedules + multi-step escalation policies**, **credentials encryption at rest** (envelope encryption with out-of-band master key), labels with autocomplete + filtering, 2FA, MCP/AI integration, browser monitoring (Rod), Prometheus metrics, dual PostgreSQL/SQLite backend, single-binary self-hosting. See "SolidPing Competitive Position" below for the full ✅/❌ inventory.

## Pricing Comparison

### Free Tier

| Service | Free Monitors | Check Interval | Status Pages | Heartbeats | Notable Limits |
|---------|---------------|----------------|--------------|------------|----------------|
| **BetterStack** | 10 monitors | 3 minutes | 1 (basic) | 10 | 3-minute checks |
| **UptimeRobot** | 50 monitors | 5 minutes | 1 (basic) | ❌ Pro only | 5-minute checks, 10 API req/min |
| **Pingdom** | ❌ No free tier | - | - | - | 14-day trial only |
| **StatusCake** | 10 monitors | 5 minutes | ✅ | ✅ Push | 75 free SMS/month |
| **Checkly** | 10k API + 1k browser runs | N/A (run-based) | ✅ | ✅ | 1 user |
| **Healthchecks.io** | 20 checks | N/A (passive) | ❌ | ✅ (core feature) | Cron/heartbeat only |

**Winner**: UptimeRobot for active monitoring (50 free monitors), Healthchecks.io for cron monitoring (20 free checks)

### Entry-Level Paid

| Service | Price/Month | Monitors | Interval | Key Features |
|---------|-------------|----------|----------|--------------|
| **BetterStack** | $25 (monitors) + $34 (responder) | 50 | 30 seconds | Modular pricing, on-call |
| **UptimeRobot** | $7 | 10 | 1 minute | Basic features |
| **Pingdom** | ~$15 | 10 | 1 minute | 1 advanced check, 50 SMS |

**Winner**: UptimeRobot ($7 vs $18 vs $10)

### Mid-Tier

| Service | Price/Month | Monitors | Interval | Key Features |
|---------|-------------|----------|----------|--------------|
| **BetterStack** | ~$59+ (modular) | 50 | 30 seconds | Modular pricing, unlimited alerts |
| **UptimeRobot** | $29-34 | 50-100 | 1 minute | Team features |
| **Pingdom** | ~$35 | 50 | 1 minute | 5 advanced checks, 500 SMS |

**Winner**: UptimeRobot ($29 vs $89 vs $32)

### Value for 100 Monitors

| Service | Price/Month | Interval | Notable Features |
|---------|-------------|----------|------------------|
| **BetterStack** | $269 | 30 seconds | 6 users, unlimited alerts, on-call |
| **UptimeRobot** | ~$29-34 | 1 minute | 100 monitors included |
| **Pingdom** | ~$50-60 | 1 minute | Transaction monitoring available |

**Winner**: UptimeRobot (best price/monitor ratio)

## Monitor Types Comparison

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
| **Domain expiration** | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ (v2.1) | ❌ | ✅ (WHOIS) |
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

**Most Comprehensive**: SolidPing (32 check types — broadest protocol coverage of any tool surveyed)

**Best Free**: UptimeRobot (8 types, 50 free monitors) for SaaS; SolidPing for self-hosted (unlimited)

**Most Flexible Conditions**: Gatus (JSONPath, conditions, external scripts), Checkly (full Playwright assertions), SolidPing (sandboxed JS)

**Enterprise Features**: Pingdom (Transaction monitoring, RUM)

## API Comparison

### Authentication

| Service | Method | Token Types | Security |
|---------|--------|-------------|----------|
| **BetterStack** | Bearer token (JWT) | Global, Team-scoped | ⭐⭐⭐⭐⭐ |
| **UptimeRobot** | Bearer token (JWT) | Account, Monitor-specific, Read-only | ⭐⭐⭐⭐⭐ |
| **Pingdom** | Bearer token | Read-only, Read/Write | ⭐⭐⭐⭐ |

**Winner**: Tie (UptimeRobot for granularity, all modern and secure)

### API Design

| Feature | BetterStack | UptimeRobot | Pingdom |
|---------|-------------|-------------|---------|
| **Architecture** | RESTful (JSON:API) | RESTful | RESTful |
| **HTTP Methods** | GET, POST, PATCH, DELETE | GET, POST, PATCH, DELETE | GET, POST, PUT, DELETE |
| **Versioning** | v2, v3 (incidents) | v3 (v2 legacy) | 3.1 (2.1 legacy) |
| **Pagination** | Links (first/last/prev/next) | Cursor-based | Offset-based |
| **Response Format** | JSON only | JSON only | JSON only |
| **Documentation** | Good | Good | JavaScript-required (poor) |
| **CORS Support** | Yes | Yes (v3) | Yes |

**Best API Design**: UptimeRobot (modern v3, cursor pagination, CORS)

**Most Consistent**: BetterStack (JSON:API spec compliance)

**Worst Documentation**: Pingdom (requires JavaScript to view)

### Rate Limits

| Service | Free Plan | Paid Plans | Headers | Documentation |
|---------|-----------|------------|---------|---------------|
| **BetterStack** | Not specified | Not specified | ❌ | ❌ Poor |
| **UptimeRobot** | 10 req/min | monitor_limit × 2 (max 5,000) | ✅ X-RateLimit-* | ✅ Excellent |
| **Pingdom** | Not specified | Not specified | ✅ (mentioned) | ❌ Poor |

**Winner**: UptimeRobot (clear limits, transparent headers)

### Available Endpoints

| Endpoint Category | BetterStack | UptimeRobot | Pingdom |
|-------------------|-------------|-------------|---------|
| **Monitors** | ✅ Full CRUD | ✅ Full CRUD | ✅ Full CRUD |
| **Monitor Groups** | ✅ | ❌ | ❌ |
| **Heartbeats** | ✅ Full CRUD | ✅ Full CRUD | ❌ |
| **Alert Contacts** | ❌ (integrations) | ✅ Full CRUD | ✅ Full CRUD |
| **Integrations** | ✅ | ✅ | ❌ (limited) |
| **Incidents** | ✅ (v3) | ✅ | ✅ (actions) |
| **Status Pages** | ✅ | ✅ (PSPs) | ❌ |
| **Maintenance Windows** | ✅ | ✅ (Pro) | ✅ |
| **Response Times** | ✅ | ❌ | ✅ |
| **Availability/SLA** | ✅ | ✅ | ✅ (summary) |
| **Probe Servers** | ❌ | ❌ | ✅ |
| **User Profile** | ❌ | ✅ (/user/me) | ❌ |

**Most Complete API**: BetterStack (monitor groups, incidents v3)

**Best Developer Experience**: UptimeRobot (user profile, clear docs)

## Key Features Comparison

### Monitoring Capabilities

| Feature | BetterStack | UptimeRobot | Pingdom |
|---------|-------------|-------------|---------|
| **Minimum Check Interval** | 30 seconds (paid) | 30 seconds (Enterprise) | 1 minute |
| **Free Tier Interval** | 3 minutes | 5 minutes | N/A |
| **Multi-location Checks** | ❌ Not mentioned | Yes (geo-verified) | ✅ 100+ locations |
| **Custom HTTP Headers** | ✅ | ✅ | ✅ |
| **HTTP Methods** | GET, POST, PUT, PATCH, DELETE | GET, POST, PUT, PATCH, DELETE, OPTIONS | GET, POST |
| **Expected Status Codes** | ✅ | ✅ | ❌ (200-299 default) |
| **Request Timeout** | ✅ Configurable | ✅ 2-60s HTTP, 500-5000ms server | ✅ 30s fixed |
| **Follow Redirects** | ✅ | ❌ Not mentioned | ❌ Not mentioned |
| **SSL Verification** | ✅ Optional | ❌ Not mentioned | ✅ Optional |

**Most Flexible**: BetterStack (all HTTP methods, follow redirects, SSL options)

**Best Global Coverage**: Pingdom (100+ locations)

### Alerting & Notifications

| Feature | BetterStack | UptimeRobot | Pingdom | StatusCake | Checkly | Healthchecks.io | Uptime Kuma | Gatus | SolidPing |
|---------|-------------|-------------|---------|------------|---------|-----------------|-------------|-------|-----------|
| **Email** | ✅ Unlimited | ✅ Unlimited | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ (SMTP) | ✅ |
| **SMS** | ✅ Unlimited | ✅ Limited | ✅ Quota | ✅ 75 free/mo | ✅ (via int.) | ✅ (Twilio) | ✅ (Twilio) | ✅ (Twilio) | ❌ |
| **Voice Calls** | ✅ Unlimited | ❌ | ✅ Limited | ✅ | ✅ (via int.) | ✅ (Twilio) | ❌ | ❌ | ❌ |
| **Slack** | ✅ Native | ✅ Native | ✅ Webhook | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ (OAuth + threads) |
| **Discord** | ✅ Native | ✅ Native | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ (OAuth + webhook) |
| **Microsoft Teams** | ✅ Native | ✅ Native | ✅ Webhook | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **Telegram** | ✅ Native | ✅ Native | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ (spec ready) |
| **PagerDuty** | ✅ Native | ✅ Native | ✅ Native | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ (spec ready) |
| **OpsGenie** | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ |
| **Google Chat** | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ |
| **Mattermost** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Webhooks** | ✅ Custom | ✅ Custom | ✅ Custom | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Push (Pushover)** | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Ntfy** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Signal** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ❌ |
| **Matrix** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ❌ |
| **Total Channels** | ~15 | ~12 | ~8 | ~14 | ~17 | ~25 | ~90 (Apprise) | ~20 | **9 native** |

**Most Channels**: Uptime Kuma (~90 via Apprise library)

**Best Native Integrations**: BetterStack & Checkly (~15-17 first-class), SolidPing (9 native, including chat-platform OAuth flows and Slack Marketplace direct install)

**SolidPing Remaining Gaps**: Microsoft Teams, Telegram, PagerDuty, SMS/Voice (Telegram, Teams, and PagerDuty specs are drafted in `specs/ideas/2026-03-22-notification-channels.md` and `specs/ideas/2026-03-22-telegram-notifications.md`)

### Advanced Features

| Feature | BetterStack | UptimeRobot | Pingdom |
|---------|-------------|-------------|---------|
| **Incident Management** | ✅ Advanced (on-call, escalation) | ❌ Basic | ❌ Basic |
| **On-Call Scheduling** | ✅ | ❌ | ❌ |
| **Escalation Rules** | ✅ | ❌ | ❌ |
| **Automatic Incident Merging** | ✅ | ❌ | ❌ |
| **Slack Incident Response** | ✅ | ❌ | ❌ |
| **Status Pages** | ✅ Custom domains | ✅ Basic | ✅ Basic |
| **Transaction Monitoring** | ✅ Playwright | ❌ | ✅ Chrome browser |
| **Real User Monitoring (RUM)** | ❌ | ❌ | ✅ JavaScript snippet |
| **Page Speed Monitoring** | ❌ | ❌ | ✅ Waterfall charts |
| **Traceroute/MTR** | ✅ For timeouts | ❌ | ❌ |
| **Screenshot Capture** | ✅ | ❌ | ❌ |
| **Mobile Apps** | ❌ Not mentioned | ✅ iOS, Android | ✅ iOS, Android |

**Best Incident Management**: BetterStack (on-call, escalation, merging)

**Best Performance Monitoring**: Pingdom (RUM, page speed, transactions)

**Best Mobile Experience**: Tie (UptimeRobot & Pingdom have native apps)

### Developer Experience

| Feature | BetterStack | UptimeRobot | Pingdom |
|---------|-------------|-------------|---------|
| **API Documentation Quality** | ⭐⭐⭐⭐ Good | ⭐⭐⭐⭐⭐ Excellent | ⭐⭐ Poor (JS-required) |
| **Terraform Provider** | ✅ | ✅ | ✅ |
| **SDK Availability** | Limited | Good (npm, Python) | Limited |
| **API Versioning** | Clear (v2, v3) | Clear (v3, v2 legacy) | Clear (3.1, 2.1) |
| **Webhook Push** | ❌ Not mentioned | ❌ | ❌ |
| **CRUD Automation** | ✅ | ✅ | ✅ |
| **Bulk Operations** | ❌ Not shown | ❌ Not shown | ✅ Pause multiple |
| **Tag/Label Support** | ❌ Not mentioned | ❌ Not mentioned | ✅ |

**Best API Docs**: UptimeRobot (clear, accessible, comprehensive)

**Best Automation**: Pingdom (bulk operations, tags)

**Worst Accessibility**: Pingdom (JavaScript-required docs)

## Pros & Cons Summary

### BetterStack Uptime

**Pros**:
- ✅ All-you-can-alert pricing (unlimited alerts)
- ✅ Advanced incident management (on-call, escalation)
- ✅ 30-second check intervals
- ✅ Traceroute/MTR diagnostics
- ✅ Playwright browser testing
- ✅ Most comprehensive protocol support
- ✅ Screenshot capture on errors
- ✅ Custom branded status pages
- ✅ JSON:API compliance

**Cons**:
- ❌ More expensive than UptimeRobot
- ❌ Only 10 free monitors (vs 50)
- ❌ 3-minute free tier intervals
- ❌ No multi-location checks mentioned
- ❌ No mobile apps mentioned
- ❌ Rate limits not documented
- ❌ No RUM or page speed monitoring

**Best For**: Modern DevOps teams needing advanced incident management and unlimited alerting

### UptimeRobot

**Pros**:
- ✅ 50 free monitors (industry-leading)
- ✅ Most affordable paid plans
- ✅ Excellent API documentation
- ✅ 3 API key types (granular access)
- ✅ Cursor pagination (scalable)
- ✅ Clear rate limits with headers
- ✅ Native mobile apps
- ✅ Multi-location geo-verified checks
- ✅ Modern v3 API

**Cons**:
- ❌ 5-minute free tier intervals
- ❌ Heartbeat monitoring requires Pro
- ❌ No email protocol monitoring (SMTP, IMAP, POP3)
- ❌ No advanced incident management
- ❌ No on-call scheduling
- ❌ 10 req/min API limit on free tier
- ❌ No transaction or browser monitoring

**Best For**: Budget-conscious users, small businesses, hobbyists, anyone needing many monitors

### Pingdom

**Pros**:
- ✅ 100+ global probe locations (best coverage)
- ✅ Transaction monitoring (real Chrome)
- ✅ Real User Monitoring (RUM)
- ✅ Page speed monitoring
- ✅ Established brand (17+ years)
- ✅ SolarWinds enterprise backing
- ✅ Native mobile apps
- ✅ Comprehensive protocol support
- ✅ Bulk operations in API

**Cons**:
- ❌ No free tier (only 30-day trial)
- ❌ Expensive ($10 for 10 monitors)
- ❌ False positive problems (user complaints)
- ❌ Alert fatigue issues
- ❌ Complex pricing (22 tiers)
- ❌ JavaScript-required API docs
- ❌ No heartbeat/cron monitoring
- ❌ 1-minute minimum interval
- ❌ SolarWinds ownership concerns

**Best For**: Enterprises with large budgets needing RUM, page speed, and transaction monitoring

## Use Case Recommendations

### For SolidPing Development

**Learn From**:
1. **UptimeRobot**:
   - Generous free tier strategy (50 monitors)
   - Clear API documentation
   - Rate limit transparency
   - Multiple API key types
   - Cursor-based pagination

2. **BetterStack**:
   - All-you-can-alert pricing model
   - Incident management features
   - JSON:API specification
   - Advanced diagnostics (traceroute)
   - Playwright integration approach

3. **Pingdom**:
   - Multi-location checking architecture
   - Comprehensive protocol support
   - Bulk operations
   - Tag-based organization

**Avoid**:
1. **Pingdom**: Expensive pricing, no free tier, false positives
2. **All Three**: Vendor lock-in (SaaS-only)
3. **BetterStack**: Limited free tier (10 monitors)
4. **UptimeRobot**: Pro-only heartbeat monitoring

### For Different User Types

| User Type | Recommendation | Reason |
|-----------|----------------|--------|
| **Hobbyist** | UptimeRobot | 50 free monitors, 5-min checks |
| **Startup** | BetterStack or UptimeRobot | Unlimited alerts vs low cost |
| **Small Business** | UptimeRobot | Best price/monitor ratio |
| **DevOps Team** | BetterStack | On-call, incident management |
| **Enterprise** | Pingdom or BetterStack | RUM/transactions vs incident mgmt |
| **Developer** | UptimeRobot | Best API docs, good free tier |
| **Agency** | UptimeRobot | Many monitors, low cost |
| **Budget-Conscious** | UptimeRobot | Cheapest at all tiers |
| **Feature-Rich** | BetterStack | Advanced features, diagnostics |

## SolidPing Competitive Position

### SolidPing Advantages Over All Three

| Advantage | vs BetterStack | vs UptimeRobot | vs Pingdom |
|-----------|----------------|----------------|------------|
| **Self-hosted** | ✅ vs SaaS | ✅ vs SaaS | ✅ vs SaaS |
| **No vendor lock-in** | ✅ | ✅ | ✅ |
| **Unlimited monitors** | ✅ (paid limits) | ✅ (plan limits) | ✅ (expensive) |
| **No recurring costs** | ✅ ($18-269/mo) | ✅ ($7-64/mo) | ✅ ($10-120+/mo) |
| **Privacy-first** | ✅ | ✅ | ✅ |
| **Direct DB access** | ✅ | ✅ | ✅ |
| **Open source potential** | ✅ | ✅ | ✅ |
| **PostgreSQL-native** | ✅ | ✅ | ✅ |
| **No false positives** | ✅ (control infra) | ✅ (control infra) | ✅ (known issue) |
| **Customizable** | ✅ | ✅ | ✅ |

### Features SolidPing Should Prioritize

Based on competitive analysis, prioritize these features:

**Tier 1 - Critical for Parity** (done):
1. ✅ HTTP/HTTPS monitoring (with JSON body validation, regex, custom UA)
2. ✅ Heartbeat/cron monitoring
3. ✅ Keyword monitoring (string + regex matching)
4. ✅ TCP / UDP port monitoring
5. ✅ Ping/ICMP monitoring
6. ✅ SSL certificate expiration alerts
7. ✅ SMTP / POP3 / IMAP monitoring
8. ✅ SSH, FTP, SFTP monitoring
9. ✅ WebSocket, gRPC monitoring
10. ✅ DNS monitoring (A, AAAA, CNAME, MX, NS, TXT)
11. ✅ Domain expiration monitoring (WHOIS-based)
12. ✅ Database monitoring (Postgres, MySQL, MSSQL, Oracle, MongoDB, Redis)
13. ✅ Message-queue monitoring (Kafka, RabbitMQ, MQTT)
14. ✅ Docker container, SNMP, A2S/Minecraft game server, custom JS check, browser (Rod) monitoring
15. ✅ Multiple notification channels — 9 native: Slack (OAuth + threads + Marketplace install), Discord (OAuth + webhook), Email, Webhooks, Google Chat, Mattermost, Ntfy, Opsgenie, Pushover
16. ✅ Public status pages with sections, resources, availability metrics, locale-aware date formatting
17. ✅ Multi-location checking (distributed workers + multi-region)
18. ✅ Monitor grouping (check groups + group-incident correlation)
19. ✅ Advanced HTTP options (custom headers, body, methods, custom user-agent)
20. ✅ Response time tracking (min/max/avg metrics, period-based aggregation, configurable retention)
21. ✅ Incident management — adaptive resolution, group-incident correlation, ack/snooze/manual-resolve
22. ✅ On-call schedules (rotations + overrides) and multi-step escalation policies
23. ✅ Audit logging / events system
24. ✅ Maintenance windows (with recurrence)
25. ✅ JSON body validation / JSONPath queries
26. ✅ 2FA / MFA (TOTP)
27. ✅ Credentials encryption at rest (envelope encryption with out-of-band master key)
28. ✅ Prometheus `/metrics` endpoint
29. ✅ Sentry integration
30. ✅ MCP (Model Context Protocol) for AI/LLM access
31. ✅ Check import/export (JSON), check clone, check templates
32. ✅ Real-time check validation, sample configs, type registry
33. ✅ Internationalization (i18n) — English + French
34. ✅ Personal Access Tokens, OAuth (Google, GitHub, GitLab, Microsoft, Slack, Discord) with per-provider enable toggle
35. ✅ Status badges (SVG)
36. ✅ Labels with autocomplete API and list-page filtering
37. ✅ Email inbox passive monitoring via JMAP (deliverability end-to-end)

**Tier 2 - High-Impact Gaps** (not yet implemented, multiple competitors offer these):
1. ❌ Status-page subscriber notifications — let end users subscribe to incident updates by email/RSS (UptimeRobot, Pingdom, Checkly, BetterStack)
2. ❌ Telegram, Microsoft Teams, PagerDuty notification channels — specs ready in `specs/ideas/2026-03-22-telegram-notifications.md` and `specs/ideas/2026-03-22-notification-channels.md`
3. ❌ Screenshot capture on HTTP failure (BetterStack, Checkly) — research done, Rod chosen, spec ready in `specs/ideas/2026-01-05-screenshots.md`
4. ❌ Configuration as Code — declarative YAML / Terraform provider (Gatus, Checkly, BetterStack)
5. ❌ Importers from BetterStack / UptimeRobot / Uptime Kuma (spec stub in `specs/ideas/2025-12-28-importers.md` — lowers switching friction)
6. ❌ Org-level check rate limiting — proportional fair scaling so one tenant cannot DoS the workers (spec ready in `specs/backlog/2026-03-30-org-check-rate-limit.md`)

**Tier 3 - Competitive Differentiators** (nice to have):
1. ❌ Page speed / Core Web Vitals monitoring (Pingdom, StatusCake)
2. ❌ Real User Monitoring / RUM (Pingdom, Site24x7)
3. ❌ Traceroute/MTR diagnostics on failure (BetterStack)
4. ❌ Mobile applications (UptimeRobot, Pingdom) or installable PWA
5. ❌ GitHub/GitLab issue integration (Gatus)
6. ❌ SMS / Voice escalations (every major SaaS via Twilio)
7. ❌ Heartbeat enhancements — `/start` endpoint, exit codes, log attachment (Healthchecks.io)
8. ❌ Automatic application discovery — suggest healthcheck endpoints from URL (spec in `specs/ideas/2025-12-28-automatic-app-discovery.md` — no competitor has this)
9. ❌ AIOps / anomaly detection on response-time series (Site24x7, Datadog)
10. ❌ Subchecks (parent HTTP check auto-spawns SSL/domain-expiration sub-checks — spec stub in `specs/ideas/2026-01-01-subchecks.md`)

### SolidPing Unique Strengths (no single competitor matches all)

| Strength | Closest Competitor |
|----------|-------------------|
| Self-hosted + Multi-tenancy + RBAC + 2FA | None (unique combination) |
| 32 check types in a single binary | Site24x7 (SaaS only); Uptime Kuma has ~12 |
| Dual PostgreSQL / SQLite + embedded Postgres | None (most OSS tools are single-DB) |
| Distributed workers + multi-region scheduling | SaaS only (BetterStack, Pingdom); not in self-hosted OSS |
| Group-incident correlation (one alert per outage, not per check) in self-hosted | BetterStack (SaaS only, "automatic incident merging") |
| Incident management with adaptive resolution + ack/snooze/manual-resolve in self-hosted | BetterStack (SaaS only) |
| On-call schedules + multi-step escalation policies in self-hosted | Opsgenie / PagerDuty (paid SaaS); BetterStack (SaaS only) |
| Credentials encryption at rest with envelope encryption | None in self-hosted category |
| Maintenance windows with recurrence in self-hosted | BetterStack, UptimeRobot (SaaS only) |
| Browser checks (Rod) self-hosted | Checkly, BetterStack (SaaS only) |
| Email inbox passive monitoring via JMAP (deliverability) | None |
| MCP server for AI/LLM tool integration | None |
| Sandboxed JavaScript checks (no external runtime) | Gatus (external script only) |
| Full audit logging / events system + Prometheus `/metrics` | BetterStack (SaaS), Gatus (metrics only) |
| OAuth multi-provider auth (Google, GitHub, GitLab, Microsoft, Slack, Discord) with per-provider toggle | None in self-hosted category |
| Slack OAuth + threaded incident messages + Marketplace direct install | BetterStack (SaaS only) |
| Labels with autocomplete + filtering + check clone + check templates | Partial in BetterStack (tags); no self-hosted match |

### Pricing Strategy Recommendation

**SolidPing SaaS Pricing** (if offered):

| Tier | Price | Monitors | Interval | Strategy |
|------|-------|----------|----------|----------|
| **Free** | $0 | 50-100 | 5 minutes | Beat UptimeRobot, crush Pingdom |
| **Starter** | $5 | 50 | 1 minute | Undercut all three |
| **Pro** | $15 | 100 | 30 seconds | 50% cheaper than BetterStack |
| **Business** | $49 | 500 | 30 seconds | Volume pricing advantage |

**Self-hosted**: Always free, unlimited monitors (main differentiator)

## Summary Comparison Table

| Criteria | BetterStack | UptimeRobot | Pingdom | SolidPing Potential |
|----------|-------------|-------------|---------|---------------------|
| **Best Free Tier** | ❌ (10 monitors) | ✅ (50 monitors) | ❌ (none) | 🎯 Beat all (100+) |
| **Best Price** | ❌ | ✅ | ❌ | 🎯 Free self-hosted |
| **Best Features** | ✅ | ❌ | ⚠️ (RUM) | 🎯 Match + self-host |
| **Best API** | ⚠️ (good) | ✅ (excellent) | ❌ (poor docs) | 🎯 Match UptimeRobot |
| **Best Incident Mgmt** | ✅ | ❌ | ❌ | ⚠️ Future SaaS feature |
| **Most Affordable** | ❌ | ✅ | ❌ | 🎯 Free self-hosted |
| **Most Reliable** | ✅ | ✅ | ❌ (false positives) | 🎯 User-controlled |
| **Best for DevOps** | ✅ | ❌ | ❌ | 🎯 Self-hosted wins |
| **Best for Startups** | ⚠️ | ✅ | ❌ | 🎯 Free unlimited |
| **Best for Enterprise** | ⚠️ | ❌ | ✅ | 🎯 Self-hosted control |

## Final Verdict

### Winner by Category

- 🥇 **Best Free Tier**: UptimeRobot (50 monitors)
- 🥇 **Best Price/Value**: UptimeRobot (all tiers)
- 🥇 **Best Features**: BetterStack (incident mgmt, diagnostics)
- 🥇 **Best API**: UptimeRobot (docs, design, transparency)
- 🥇 **Best for Enterprises**: Pingdom (RUM, page speed, transactions)
- 🥇 **Best Incident Management**: BetterStack (on-call, escalation)
- 🥇 **Best Global Coverage**: Pingdom (100+ locations)
- 🥇 **Best Developer Experience**: UptimeRobot (docs, API, mobile)

### Overall Winner: **UptimeRobot**
**Reason**: Best balance of price, features, and API quality. 50 free monitors crush competition.

### Best Premium Option: **BetterStack**
**Reason**: If paying, get the best features (incident mgmt, unlimited alerts, diagnostics).

### Avoid: **Pingdom**
**Reason**: Expensive, no free tier, false positives, declining market position.

### SolidPing Opportunity: **Beat Them All**
**Strategy**:
1. Self-hosted = free + unlimited (beats all on price)
2. Match UptimeRobot's API quality (excellent docs, clear limits)
3. Add BetterStack's diagnostic features (screenshots — spec ready; traceroute remaining)
4. Skip Pingdom's mistakes (no false positives, no complex pricing)
5. Offer optional SaaS with pricing that undercuts UptimeRobot

**Today (May 2026)**: SolidPing covers parity on (1), (2), and most protocol/feature breadth, and has overtaken BetterStack on (3) for self-hosted incident management — group-incident correlation, on-call schedules, multi-step escalation policies, ack/snooze/manual-resolve, and credentials encryption are all in. Remaining items are: status-page subscriber notifications, Telegram/Teams/PagerDuty channels, screenshots, importers, and a Terraform provider.

**Result**: Best of all worlds — self-hosted freedom with optional affordable SaaS.
