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
