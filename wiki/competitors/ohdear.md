# Oh Dear - Analysis

Snapshot taken 2026-10-03 from ohdear.app (pricing page, features page, docs index,
notifications docs, the `oh-dear-vs-pingdom` page and the August 2025 changelog).
Updated 2026-10-12 from the "Uptime and performance monitoring" docs page (see
[Uptime check details](#uptime-check-details)).

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
| Uptime / performance | HTTP GET/POST/PUT/PATCH, headers, payload, required/forbidden strings, header and JSON assertions, redirect target, 1-10 s timeout |
| Ping | ICMP / ICMPv6 (since 2025-08), loss % and average RTT thresholds |
| TCP port | Connect, banner and send/expect strings (since 2025-08) |
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

- **33 checker servers in 16 cities, 6 continents**: Cape Town, Bangalore, Seoul,
  Singapore, Tokyo, Sydney, Toronto, Frankfurt, London, Paris, Stockholm, Sao Paulo,
  New York, Dallas, Los Angeles, San Francisco.
- Interval 1 to 60 minutes per monitor. **No sub-minute checks.**
- Each monitor has a **primary** location (user-chosen). When the primary fails, a
  **secondary** location re-checks right away. It is picked at random, never the same
  one twice in a row, same continent preferred.
  - Both fail: the run is failed and counts toward the alert threshold.
  - Only the primary fails: the run is a **warning** ("Partially unreachable"). It
    counts as online for uptime %, records no downtime, sends nothing.
  - Primary up: success, no secondary run.
- Alert threshold 1-20 consecutive failed minutes, default 2.
- Monthly uptime % is computed from the **primary** location only.
- No private / on-prem agents.

## Uptime check details

From the uptime and performance docs page, 2026-10-12.

**HTTP**
- Pass criterion is any **2xx** on the final response. Redirects are followed.
- Timeout 1-10 s, default 5 s.
- Methods GET (default), POST, PATCH, PUT. Payload as form fields or raw JSON (validated
  and auto-formatted on save, 10,000 characters max, `Content-Type` set automatically).
- Up to 5 custom request headers. Basic auth is done through a header.
- Required string and absent string on the body.
- Response header checks: exact value, `*` wildcard, exists, not exists (catches a
  leaked `X-Powered-By`), not contains (passes when the header is absent, e.g. watch
  `X-Robots-Tag` for `noindex`).
- JSON body assertions: dot path with `*` and array indexes; equals, not equals,
  contains, greater than, less than, exists, does not exist. Body over 2 MB or invalid
  JSON fails the check. "Test against your URL" runs them live, and clicking a value in
  the response creates an assertion.
- Redirect target: assert the site redirects to a given URL.
- Cache-busting variables in URL, headers and payload: `$OH_DEAR_TIMESTAMP`,
  `$OH_DEAR_ISO8601`, `$OH_DEAR_UUID`, `$OH_DEAR_RANDOM`. Same value within one run. The
  expanded URL is shown on the result.

**Ping**: count, interval, timeout, packet size, max hops, acceptable packet loss %,
acceptable average RTT.

**TCP**: timeout (default 1000 ms), string in the welcome banner, send string (`\r\n`
escapes), string in the reply to the send string. Banner and reply are separate
expectations.

**Performance**
- Timing phases recorded and charted: DNS, TCP connect, TLS, server processing (TTFB),
  content download.
- Alerts on a hard threshold (consistently slower than X ms) and on a sudden change in %
  (average of the last 15 min vs the last 2 h, faster or slower).

**Failure reports and alerts**
- Timeout message with bytes received, full response headers on non-2xx.
- Notifications name the location and its IPv4/IPv6 addresses, and the verifying
  location.
- A copy-paste `curl -i` command reproduces the check.
- Reminder every hour once the monitor has been down for an hour. Acknowledging stops
  reminders, the recovery notification is still sent.
- Monthly uptime email per monitor.

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
| Website-quality checks | broken links, mixed content, Lighthouse, sitemap, AI checks | ❌ |
| DNS blocklist | ✅ | ✅ (`checkdnsbl`) |
| Probe locations | 33 servers / 16 cities | 6 shared regions + private agents |
| Failure confirmation | immediate re-check from a second location | region quorum (`failQuorum`) over a confirmation period (120 s default) |
| HTTP default pass | any 2xx | exactly 200 (spec 2026-10-12-09) |
| HTTP methods | GET, POST, PUT, PATCH | + DELETE, HEAD, OPTIONS, QUERY |
| HTTP version pinning | ❌ | ✅ (`httpVersion` 1.1 / 2 / 3) |
| JSON assertions | dot path, 7 operators | full JSONPath, 11 operators, regex, and/or groups |
| Header assertions | exact, wildcard, exists, not exists, not contains | regex only (spec 2026-10-12-11) |
| Redirect target assertion | ✅ | ❌ (spec 2026-10-12-12) |
| Cache-busting variables | ✅ | ❌ (spec 2026-10-12-13), `${param:}` / `${env:}` only |
| Encrypted secret headers | ❌ | ✅ (`secretHeaders`, `basicAuth`) |
| HTTP timing phases | DNS, TCP, TLS, TTFB, download | total only (spec 2026-10-12-08) |
| Copy as cURL | ✅ | heartbeat only (spec 2026-10-12-10) |
| Slow-response alert | hard threshold + % change | hard threshold as a degraded incident (`slowThresholdMs`), % change is a non-goal |
| ICMP loss / RTT thresholds | ✅ | ❌ (spec 2026-10-12-14) |
| Region / IP in notifications | location, IPs, verifying location | ❌ (spec 2026-10-12-15, no IPs) |
| Reminders on open incidents | hourly | ❌ outside escalation policies (spec 2026-10-12-16) |
| On-call / escalation | ❌ (delegated) | ✅ schedules, rotations, escalation |
| SMS / voice | SMS | ❌ on Cloud |
| Telegram | ✅ | ✅ as a personal user contact, not an org channel |
| Microsoft Teams | ✅ | ✅ (`msteams`, `msteamsbot`) |
| Opsgenie | ✅ | ❌ (sender removed in v0.17.0) |
| Uptime reports | monthly email, PDF, client reports | weekly / monthly HTML email, no PDF |
| Self-host | ❌ | ✅ AGPL |

## Takeaways for SolidPing

1. **Different buyer.** Oh Dear sells website health to agencies and Laravel shops
   who manage client sites. SolidPing sells infrastructure monitoring breadth. The
   overlap is the uptime + cert + cron + status page core.
2. **Price is the obvious wedge.** Per-monitor pricing makes 200 client sites cost
   $439/mo. That is exactly the "agency with 200 sites @5-min" buyer the
   throughput model was designed for.
3. **Gaps they expose:** crawl-based checks (broken links, mixed content), Lighthouse,
   PDF client reports, Opsgenie. Crawling is a product-scope decision.
4. **HTTP check depth (2026-10-12 review).** Our assertion engine is stronger, but they
   measure and explain more: timing phases, copy as cURL, header not-exists/not-contains,
   redirect target, cache-busting variables, ICMP thresholds, region in alerts, hourly
   reminders. Specs `2026-10-12-08` to `2026-10-12-16` cover these.
5. **Their docs explain the basics better.** One page says what counts as downtime and
   walks through an alert timeline minute by minute. Ours had the old count-based
   thresholds until 2026-10-12.

## Sources

- https://ohdear.app/pricing
- https://ohdear.app/features
- https://ohdear.app/docs
- https://ohdear.app/docs/notifications
- https://ohdear.app/docs/features/uptime-and-performance-monitoring
- https://ohdear.app/oh-dear-vs-pingdom.md
- https://ohdear.app/news-and-updates/introducing-ping-and-tcp-port-monitoring
