# Pingdom - Complete Analysis

## Overview

Pingdom (a SolarWinds product) is an established website monitoring service that provides uptime, transaction, page speed, and real user monitoring (RUM). Originally founded in 2007, Pingdom has become a well-known name in the monitoring space, though it has faced increasing competition from newer, more affordable alternatives.

**Base API URL**: `https://api.pingdom.com/api/3.1/`

**API Specification**: RESTful HTTP-based API with Bearer token authentication

**Current Owner**: SolarWinds (acquired Pingdom in 2014). SolarWinds itself was taken private by Turn/River Capital in early 2025 (previously majority-owned by Silver Lake and Thoma Bravo since 2016).

## Files in this directory

- [monitoring.md](monitoring.md) — Check types in-depth (HTTP, transaction, ping, TCP/UDP, DNS, mail server) and the global probe network (regions, selection, access).
- [api.md](api.md) — API architecture (versions, auth, rate limiting, standards), core API endpoints (3.1), and the complete API reference (endpoints summary, types, statuses, intervals, regions, HTTP codes).
- [pricing.md](pricing.md) — Synthetic and RUM plans, free trial, billing, monitor limits, pain points.
- [comparison.md](comparison.md) — vs SolidPing (similarities, advantages each way, feature gaps), technical considerations (rate limiting, versioning, retention, response times, security, multi-location), limitations and gaps, and API design patterns to adopt / consider / avoid.
- [examples.md](examples.md) — Integration examples (HTTP, TCP, DNS, SMTP monitor creation, results, summary, bulk pause, maintenance, probes).
- [sources.md](sources.md) — Source URLs grouped by topic.

## At a glance

| Aspect | Pingdom |
|---|---|
| Founded | 2007 (acquired by SolarWinds in 2014) |
| Pricing | $10 → $32 → $63 → $120/mo + Enterprise; no free tier (30-day trial) |
| Min interval | 1 minute |
| Probe regions | 5 (NA, EU, APAC, LATAM, World) over 100+ probe servers |
| Check types | HTTP/HTTPS, HTTP Custom, TCP, UDP, ping, DNS, SMTP, POP3/POP3S, IMAP/IMAPS, transaction, page speed, RUM |
| API | `https://api.pingdom.com/api/3.1/` — Bearer token auth (3.1); legacy 2.1 with Basic auth still supported |
| Notable | 100+ probe locations · transaction monitoring (real Chrome) · RUM via JS snippet · bulk pause via comma-joined IDs · summary endpoints (average/outage/performance) · `probe_filters` for region selection |

## Key Features

### Monitoring Capabilities

Pingdom supports multiple monitoring types across three main product lines:

**Synthetic Monitoring** (Uptime Checks):
- **HTTP/HTTPS monitoring** - Website uptime with GET/POST requests
- **HTTP Custom** - Server-side script integration for custom monitoring
- **Ping (ICMP) monitoring** - Server connectivity checks
- **TCP monitoring** - Port connectivity with custom string verification
- **UDP monitoring** - UDP port connectivity checks
- **DNS monitoring** - DNS server functionality verification
- **SMTP monitoring** - Mail server monitoring (default 220 response code)
- **POP3/POP3S monitoring** - POP3 mail server checks
- **IMAP/IMAPS monitoring** - IMAP mail server verification

**Transaction Monitoring**:
- Real Chrome browser-based testing
- User interaction simulation
- Shopping cart flows
- Login processes
- Registration workflows
- Search functionality testing
- URL hijacking detection
- Complex multi-step scenarios

**Page Speed Monitoring**:
- Load time tracking
- Element-by-element analysis
- Performance bottleneck identification
- Size and load time metrics
- Waterfall charts
- Optimization recommendations

**Real User Monitoring (RUM)**:
- Real-time user experience data
- Geographic location insights
- Browser-based performance
- Device-specific metrics
- Actual visitor interaction tracking
- JavaScript snippet integration

### Advanced Features

- **Multi-location monitoring** - 100+ global probe servers
- **Regional selection** - 5 probe regions (North America, Europe, Asia Pacific, etc.)
- **Minute-by-minute checks** - Up to 60-second intervals (1-minute minimum)
- **30-second timeout** - For HTTP(S), HTTP Custom, DNS, and TCP checks
- **Custom headers** - HTTP request customization
- **POST data support** - Form data in HTTP checks
- **String verification** - Search for specific HTML strings
- **Basic/custom authentication** - HTTP authentication support

### Notification System

Pingdom offers multiple alerting channels:
- **Email** - Standard email notifications
- **SMS** - Text message alerts
- **Push notifications** - iOS and Android mobile apps
- **Slack** - Channel notifications
- **PagerDuty** - Incident management integration
- **Webhooks** - Custom HTTP callbacks
- **VictorOps** (now Splunk On-Call)
- **OpsGenie**
- **HipChat** (legacy)
- **Microsoft Teams** - Via webhooks

### Status Pages

- **Public status pages** - Customer-facing incident communication
- **Custom branding** - Brand customization options
- **Subscriber notifications** - Email notifications for subscribers
- **Incident history** - Historical incident reporting

### Platform Features

- **Mobile apps** - Native iOS and Android applications
- **30-day free trial** - No credit card required
- **100+ probe locations** - Global monitoring network
- **API access** - All plans include API access
- **Reporting** - Customizable performance reports
- **Integrations** - Third-party tool connectivity
