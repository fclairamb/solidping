# Uptime Monitoring Comparison — Key Features

## Monitoring Capabilities

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

#### Minimum check interval — full picture (added 2026-08-01)

The table above covers only the three SaaS incumbents. The self-hosted side is
where this feature actually differentiates, so the numbers are collected here
with their sources. **Verify claims against implementations** — the entry that
prompted this table is a vendor advertising 1-second checks while shipping 60.

| Tool | Minimum interval | Gated behind a paid tier? | Source |
|---|---|---|---|
| **SolidPing** | **10 seconds** | No — self-hosted, unlimited | `GlobalMinPeriod = 10 * time.Second`, `server/internal/checkers/checkerdef/types.go:240` |
| Uptime Kuma | ~20 seconds | No — self-hosted | user reports, incl. OneUptime#2937 |
| OneUptime | **1 minute self-hosted** (product page advertises 1 second) | n/a — the advertised figure is not reachable self-hosted | [OneUptime#2937](https://github.com/OneUptime/oneuptime/issues/2937), open, 2026-07-30, v11.7.3 Docker Compose |
| BetterStack | 30 seconds | Yes (paid); free tier 3 min | comparison table above |
| UptimeRobot | 30 seconds | Yes (Enterprise); free tier 5 min | comparison table above |
| Pingdom | 1 minute | Yes | comparison table above |
| Hyperping | 30 seconds | Yes — Essentials $24/mo | pricing snapshot |
| Checkly | 2 min (Hobby) → sub-min paid | Yes | pricing snapshot |
| exit1.dev | 30 sec (Pro) / 15 sec (Agency) | Yes | pricing snapshot |

SolidPing's 10-second floor applies to any check type that does not declare a
stricter `MinPeriod`; the deliberate exceptions are SSL (1h), domain expiry (6h)
and JS scripting (30s). Sub-10s is explicitly out of scope for the
results/aggregation model (spec `2026-07-01-04`) — the floor is an engineering
constraint, not a packaging decision, which is why it is not tier-gated.

**Accuracy note for anyone writing copy from this table:** 10 seconds is not the
fastest figure in the market (SaaS vendors sell 1-second checks). The defensible
statement is the *combination* — sub-minute **and** self-hosted **and** unlimited
**and** unpaywalled — not a superlative.

### Migration importers (added 2026-08-01)

SolidPing ships config importers for three competitors, in
`server/internal/handlers/checks/importers/`:

| Source tool | Importer | Notes |
|---|---|---|
| Uptime Kuma | `uptimekuma.go` | maps `interval` → check period; converts Kuma's `maxretries × retryInterval` retry model into a confirmation period |
| Gatus | `gatus.go` | parses YAML endpoints; defaults to `60s` when interval is unset |
| Better Stack | `betterstack.go` | — |

This covers the #1 and #2 self-hosted incumbents by stars (Uptime Kuma ~89k★,
Gatus ~11.5k★) plus one major SaaS, and is the operational answer whenever a
migration pool opens up (Freshping's 2026-03 shutdown, Peekaping's stall).


## Alerting & Notifications

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
| **Web Push** (browser, no app) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Ntfy** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Signal** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ❌ |
| **Matrix** | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ❌ |
| **Total Channels** | ~15 | ~12 | ~8 | ~14 | ~17 | ~25 | ~90 (Apprise) | ~20 | **10 native** |

**Most Channels**: Uptime Kuma (~90 via Apprise library)

**Best Native Integrations**: BetterStack & Checkly (~15-17 first-class), SolidPing (10 native, including chat-platform OAuth flows and Slack Marketplace direct install)

**SolidPing Remaining Gaps**: Microsoft Teams, Telegram, PagerDuty, SMS/Voice (Telegram, Teams, and PagerDuty specs are drafted in `specs/ideas/2026-03-22-notification-channels.md` and `specs/ideas/2026-03-22-telegram-notifications.md`)

## Advanced Features

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
| **Private Locations** (customer-hosted agent) | ❌ Squid-proxy workaround | ❌ | ❌ (successor product only) |

**Best Incident Management**: BetterStack (on-call, escalation, merging)

**Best Performance Monitoring**: Pingdom (RUM, page speed, transactions)

**Best Mobile Experience**: Tie (UptimeRobot & Pingdom have native apps)

## Developer Experience

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
