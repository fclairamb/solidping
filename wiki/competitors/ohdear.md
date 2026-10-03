# Oh Dear - Analysis

Snapshot taken 2026-10-03 from ohdear.app (pricing page, features page, docs index,
notifications docs, the `oh-dear-vs-pingdom` page and the August 2025 changelog).

## Overview

Oh Dear is a hosted website-monitoring service from Belgium, well known in the
Laravel/PHP ecosystem. It started as "uptime + certificates + broken links" and has
grown into a website-health suite: crawling, Lighthouse, sitemap, DNS, domain expiry,
cron heartbeats, application health. In August 2025 it renamed "Site" to "Monitor"
and added ping and TCP monitor types.

**Website**: https://ohdear.app

**Self-hosted**: ❌ (SaaS only)

**Open source**: ❌ (the product; client packages are open)

## Pricing

Priced **per monitor, all features on every tier**. USD, excl. VAT, annual billing
gives two months free.

| Plan | Monitors | $/month |
|---|---|---|
| Solo | 2 | 17 |
| Solo Plus | 5 | 32 |
| Freelance | 10 | 55 |
| Studio | 25 | 109 |
| Agency | 50 | 165 |
| Agency Plus | 75 | 219 |
| Portfolio | 100 | 275 |
| Portfolio Plus | 150 | 365 |
| Scale | 200 | 439 |
| Enterprise | 200+ | custom |

- 10-day trial, no credit card. No permanent free tier.
- 30-day money-back guarantee.
- Unlimited team members, unlimited notification destinations, SSO and 2FA on every plan.
- Roughly **$2.2-8.5 per monitor per month**, decreasing with volume.
- Their own comparison pages still quote "from €15", the pricing page now says
  $17 for 2 monitors. Trust the pricing page.

## Monitor types

Three probe families (HTTP, ICMP ping, TCP connect), plus a large layer of
website-quality checks run against HTTP monitors:

| Check | Notes |
|---|---|
| Uptime / performance | HTTP GET/POST/PUT/PATCH, headers, payload, required/forbidden strings, JSON assertions, 1-10 s timeout |
| Ping | ICMP / ICMPv6 (since 2025-08) |
| TCP port | Connect check (since 2025-08) |
| Certificate health | TLS validity and expiry |
| Broken links | Full-site crawl |
| Mixed content | Full-site crawl |
| Lighthouse | Scheduled audits |
| Sitemap | Sitemap validity |
| DNS records | Change detection |
| DNS blocklist | Listing on DNSBLs |
| Domain expiry | |
| Open ports | Port scanner |
| Scheduled tasks | Cron heartbeat |
| Application health | App exposes a JSON health endpoint (first-party Laravel/PHP packages) |
| AI checks | Natural-language assertions on page content |

No DNS-protocol probe beyond records, no SMTP/IMAP/POP3, SSH, FTP, UDP, gRPC,
WebSocket, database, message-queue, SNMP or scripted/browser checks.

## Check network

- **33 checker servers in 16 cities, 6 continents.**
- Interval 1 to 60 minutes per monitor. **No sub-minute checks.**
- A failure must be **confirmed from a second location** (different city and provider)
  before alerting. Alert threshold 1-20 min of downtime, default 2.
- No private / on-prem agents.

## Alerting and incidents

Channels: email, Slack, SMS (via Vonage), Discord, Telegram, Microsoft Teams,
Pushover, ntfy, Opsgenie, Google Chat, PagerDuty, webhooks.

**No on-call or incident management.** Their own vs-Pingdom table lists
"Incident management / on-call: No". Escalation is delegated to PagerDuty/Opsgenie.

## Other features

- Status pages with custom domains on every plan.
- Monthly uptime reports per team member, PDF export, automated client reports,
  custom branding, scoped client access (agency-oriented).
- Full API. A Pulumi provider exists in the Pulumi registry.

## vs SolidPing Cloud

Compare against SolidPing **Cloud** (hosted vs hosted), per
[comparison/solidping-position.md](comparison/solidping-position.md).

| | Oh Dear | SolidPing Cloud |
|---|---|---|
| Pricing axis | per monitor | checks per minute (100 to 5,000 monitors per tier) |
| 100 monitors @1 min | $275/mo | €15/mo (100 checks/min tier) |
| 200 monitors @5 min | $439/mo | €15/mo (40 checks/min) |
| Free tier | 10-day trial | permanent free plan |
| Fastest interval | 1 min | sub-minute, within the checks/min budget |
| Probe families | HTTP, ICMP, TCP | ~40 (DNS, mail, SSH, DB, MQ, gRPC, browser, JS...) |
| Website-quality checks | broken links, mixed content, Lighthouse, sitemap, DNSBL, AI checks | ❌ |
| Probe locations | 33 servers / 16 cities, 2-location confirmation | 6 shared regions + private agents |
| On-call / escalation | ❌ (delegated) | ✅ schedules, rotations, escalation |
| SMS / voice | SMS | ❌ on Cloud |
| Telegram / Teams / Opsgenie | ✅ | ❌ |
| Client reports (PDF) | ✅ | ❌ |
| Self-host | ❌ | ✅ AGPL |

## Takeaways for SolidPing

1. **Different buyer.** Oh Dear sells website health to agencies and Laravel shops
   who manage client sites. SolidPing sells infrastructure monitoring breadth. The
   overlap is the uptime + cert + cron + status page core.
2. **Price is the obvious wedge.** Per-monitor pricing makes 200 client sites cost
   $439/mo. That is exactly the "agency with 200 sites @5-min" buyer the
   throughput model was designed for.
3. **Gaps they expose:** crawl-based checks (broken links, mixed content), Lighthouse,
   monthly client reports, Telegram/Teams, two-location confirmation before alerting.
   Reports and Telegram/Teams are cheap to close. Crawling is a product-scope decision.

## Sources

- https://ohdear.app/pricing
- https://ohdear.app/features
- https://ohdear.app/docs
- https://ohdear.app/docs/notifications
- https://ohdear.app/oh-dear-vs-pingdom.md
- https://ohdear.app/news-and-updates/introducing-ping-and-tcp-port-monitoring
