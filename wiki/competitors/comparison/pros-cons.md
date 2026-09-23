# Uptime Monitoring Comparison — Pros & Cons by Vendor

## BetterStack Uptime

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

## UptimeRobot

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

## Pingdom

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

## Pulsetic

**Pros**:
- ✅ Dependency monitoring across 4,300+ third-party providers — unique in this set
- ✅ Real User Monitoring bundled (Core Web Vitals + JS error tracking), not a separate SKU
- ✅ Status pages: custom domain, SSO/password gating, translations, PDF export, badges
- ✅ Coherent white-label story — one account, many branded client pages, no client logins
- ✅ Cheap and legible: $9 / $19 / $49 with published per-unit add-on rates
- ✅ 30-second checks from 15 regions on Team ($19)
- ✅ Rate limit scales with the plan (`monitors × 3/min`, capped at 7,000/min)
- ✅ First-party MCP server ([`designmodo/pulsetic-mcp`](https://github.com/designmodo/pulsetic-mcp), MIT)

**Cons**:
- ❌ Six probe families behind thirteen marketing names — no DNS, SMTP, IMAP/POP3, SSH, FTP, gRPC, WebSocket, UDP, database, queue or SNMP checks
- ❌ No scripted browser transactions (screenshots are captures, not flows)
- ❌ No self-hosting and no private locations — nothing behind a VPN is reachable
- ❌ API is Team-and-up, so the $9 plan is click-ops only
- ❌ API path is unversioned — no migration story
- ❌ Free tier includes zero status pages
- ❌ Add-on pricing is linear and uncapped
- ❌ On-call schedules / escalation policies unverified — no product page found
- ❌ One product in a design-tooling portfolio (Designmodo), not a monitoring company

**Best For**: Agencies and small teams with an HTTP-shaped stack who want uptime, a branded status page and third-party incident awareness on one cheap bill

Full analysis: [../pulsetic.md](../pulsetic.md)
