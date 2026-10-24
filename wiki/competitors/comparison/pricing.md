# Uptime Monitoring Comparison — Pricing

> **Refresh 2026-10-15** (pricing pages read that day; UptimeRobot, Pingdom and
> Oh Dear were served in EUR to a French IP, so their numbers below are EUR and
> not comparable with older USD rows). What moved since the June–September
> snapshots:
>
> - **UptimeRobot** restructured: Free €0 (50 monitors, 5 min, now *without*
>   heartbeat, SSL/domain expiry, DNS, UDP, API monitoring, multi-location and
>   maintenance windows: "personal projects"), Solo from €10 (10 or 50 monitors,
>   60 s), Team €41 (100 monitors, **30 s**, 3 seats), new **Scale** from €77
>   (200 or 500 monitors, **15 s**), Enterprise custom (15 s standard). MCP page
>   in the nav. Source: uptimerobot.com/pricing.md.
> - **Checkly** cut included runs (Starter $24: 25k API / 3k browser; Team $64:
>   100k / 12k), offers **10 s checks** (1 s on request), and unbundled status
>   pages ($0/$9/$30) and AI root-cause analysis ($0/$12/$39). MCP on every plan.
> - **Hyperping** same prices; Pro 100 → 250 monitors, more seats, phone calls
>   from Essentials, server agents, logs (alpha), "Ask Hyperping" AI, MCP on every
>   paid plan, 18 regions.
> - **Better Stack** same uptime prices; adds AI SRE (usage-billed), an MCP
>   server, Slack/Teams incident management ($9/responder), error tracking.
> - **Pulsetic** same prices; Free now has **3 status pages**, API/MCP/n8n from
>   Solo ($9), WhatsApp alerts, AI assistant.
> - **StatusCake** same prices; the Free plan has **no** SMS credits (75/month is
>   Superior; Business 400). The old "75 free SMS" line was wrong.
> - **Pingdom** dropped tier names for a 22-step quantity slider: 10 checks €16/mo,
>   50 €66, 100 €120, 300 €342 (monthly).
> - **Uptime.com** now publishes prices: 10 checks $9/mo ($84/yr), 50 $38,
>   100 $75; 1-minute checks.
> - **Site24x7** renamed its plans (Web Uptime $10, Web Perf $39, Professional
>   $49, MSP $59) and the page says "Site24x7 Observability is now OpManager
>   Nexus".
> - **Oh Dear** now prices in EUR (Solo €15 → Scale €399), same tier sizes.
> - No change: Cronitor, Healthchecks.io.
>
> **The pattern:** AI root-cause analysis appeared on four pricing pages at once
> (Better Stack, Hyperping, Checkly, Pulsetic), and MCP is now on five. Fastest
> interval and its price, as of today:
>
> | Service | Fastest check | Cheapest plan that has it |
> |---|---|---|
> | SolidPing | 10 s | free, self-hosted, every check |
> | Checkly | 10 s (1 s on request) | Team, $64/mo |
> | UptimeRobot | 15 s | Scale, from €77/mo (€65 annual) |
> | Hyperping | 20 s | Business, $249/mo |
> | StatusCake | 30 s | Business, €69.99/mo |
> | Site24x7 | 30 s | Enterprise Plus Web, $999/mo |
>
> The comparison line is now **"10 s vs 15 s"**, not "vs 30 s".

## Free Tier

| Service | Free Monitors | Check Interval | Status Pages | Heartbeats | Notable Limits |
|---------|---------------|----------------|--------------|------------|----------------|
| **BetterStack** | 10 monitors | 3 minutes | 1 (basic) | 10 | 3-minute checks |
| **Hyperping** | 20 monitors | 5 minutes | 1 (basic) | ✅ included | No browser checks; HTTP/Port/Ping/Keyword only |
| **UptimeRobot** | 50 monitors | 5 minutes | 1 (basic) | ❌ paid only | 5-minute checks; no SSL, DNS, UDP, API or multi-location monitoring (2026-10-15) |
| **Pingdom** | ❌ No free tier | - | - | - | 14-day trial only |
| **StatusCake** | 10 monitors | 5 minutes | ✅ | ✅ Push | No SMS on Free (75/month is Superior; corrected 2026-10-15) |
| **Checkly** | 10k API + 1k browser runs | N/A (run-based) | ✅ | ✅ | 1 user |
| **Healthchecks.io** | 20 checks | N/A (passive) | ❌ | ✅ (core feature) | Cron/heartbeat only |
| **Pulsetic** | 10 monitors | 5 minutes | 3 (since 2026-10) | ✅ (shares the monitor pool) | 3 regions, 1 webhook/monitor, no API, 1k RUM page views |

**Winner**: UptimeRobot for active monitoring (50 free monitors), Healthchecks.io for cron monitoring (20 free checks)

~~**Worst-designed free tier**: Pulsetic — status pages are the product's best feature and the free plan has zero of them.~~ Fixed by Pulsetic: its Free plan has 3 status pages (2026-10-15).

## Entry-Level Paid

| Service | Price/Month | Monitors | Interval | Key Features |
|---------|-------------|----------|----------|--------------|
| **BetterStack** | $25 (monitors) + $34 (responder) | 50 | 30 seconds | Modular pricing, on-call |
| **UptimeRobot** | €10 (€9 annual) | 10 | 60 seconds | Solo; €22 for 50 monitors (2026-10-15) |
| **Pingdom** | ~$15 | 10 | 1 minute | 1 advanced check, 50 SMS |
| **Pulsetic** | $9 (Solo) | 10 | 60 seconds | 5 regions, 3 status pages, 30 SMS/calls — **no API** |

**Winner**: UptimeRobot ($7) on price; Pulsetic ($9) if you want status pages bundled at entry

## Mid-Tier

| Service | Price/Month | Monitors | Interval | Key Features |
|---------|-------------|----------|----------|--------------|
| **BetterStack** | ~$59+ (modular) | 50 | 30 seconds | Modular pricing, unlimited alerts |
| **UptimeRobot** | €41 (€35 annual) | 100 | 30 seconds | Team, 3 seats (2026-10-15) |
| **Pingdom** | ~$35 | 50 | 1 minute | 5 advanced checks, 500 SMS |
| **Pulsetic** | $19 (Team) | 50 | 30 seconds | 15 regions, unlimited status pages, 100 SMS/calls, API unlocked |

**Winner**: Pulsetic ($19 for 50 monitors at 30-sec from 15 regions) undercuts UptimeRobot at this bracket

## Value for 100 Monitors

| Service | Price/Month | Interval | Notable Features |
|---------|-------------|----------|------------------|
| **BetterStack** | $269 | 30 seconds | 6 users, unlimited alerts, on-call |
| **UptimeRobot** | €41 (€35 annual) | 30 seconds | Team, 100 monitors (2026-10-15) |
| **Pingdom** | ~$50-60 | 1 minute | Transaction monitoring available |
| **Pulsetic** | $29 ($19 + 50 × $0.20) | 30 seconds | Add-on monitors are linear and uncapped |

**Winner**: UptimeRobot (best price/monitor ratio); Pulsetic is close and buys a faster interval

> **Read the `+` signs.** Every Pulsetic plan is a floor, not a bundle: monitors are $0.20/mo
> each, SMS/calls $0.10, teammates $8, status-page subscribers $0.01. 500 monitors on
> Organization is $49 + 200 × $0.20 = **$89/mo**. See [../pulsetic.md](../pulsetic.md).
