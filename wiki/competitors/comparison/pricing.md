# Uptime Monitoring Comparison — Pricing

## Free Tier

| Service | Free Monitors | Check Interval | Status Pages | Heartbeats | Notable Limits |
|---------|---------------|----------------|--------------|------------|----------------|
| **BetterStack** | 10 monitors | 3 minutes | 1 (basic) | 10 | 3-minute checks |
| **Hyperping** | 20 monitors | 5 minutes | 1 (basic) | ✅ included | No browser checks; HTTP/Port/Ping/Keyword only |
| **UptimeRobot** | 50 monitors | 5 minutes | 1 (basic) | ❌ Pro only | 5-minute checks, 10 API req/min |
| **Pingdom** | ❌ No free tier | - | - | - | 14-day trial only |
| **StatusCake** | 10 monitors | 5 minutes | ✅ | ✅ Push | 75 free SMS/month |
| **Checkly** | 10k API + 1k browser runs | N/A (run-based) | ✅ | ✅ | 1 user |
| **Healthchecks.io** | 20 checks | N/A (passive) | ❌ | ✅ (core feature) | Cron/heartbeat only |
| **Pulsetic** | 10 monitors | 5 minutes | ❌ **none** | ✅ (shares the monitor pool) | 3 regions, 1 webhook/monitor, no API, 1k RUM page views |

**Winner**: UptimeRobot for active monitoring (50 free monitors), Healthchecks.io for cron monitoring (20 free checks)

**Worst-designed free tier**: Pulsetic — status pages are the product's best feature and the free plan has zero of them.

## Entry-Level Paid

| Service | Price/Month | Monitors | Interval | Key Features |
|---------|-------------|----------|----------|--------------|
| **BetterStack** | $25 (monitors) + $34 (responder) | 50 | 30 seconds | Modular pricing, on-call |
| **UptimeRobot** | $7 | 10 | 1 minute | Basic features |
| **Pingdom** | ~$15 | 10 | 1 minute | 1 advanced check, 50 SMS |
| **Pulsetic** | $9 (Solo) | 10 | 60 seconds | 5 regions, 3 status pages, 30 SMS/calls — **no API** |

**Winner**: UptimeRobot ($7) on price; Pulsetic ($9) if you want status pages bundled at entry

## Mid-Tier

| Service | Price/Month | Monitors | Interval | Key Features |
|---------|-------------|----------|----------|--------------|
| **BetterStack** | ~$59+ (modular) | 50 | 30 seconds | Modular pricing, unlimited alerts |
| **UptimeRobot** | $29-34 | 50-100 | 1 minute | Team features |
| **Pingdom** | ~$35 | 50 | 1 minute | 5 advanced checks, 500 SMS |
| **Pulsetic** | $19 (Team) | 50 | 30 seconds | 15 regions, unlimited status pages, 100 SMS/calls, API unlocked |

**Winner**: Pulsetic ($19 for 50 monitors at 30-sec from 15 regions) undercuts UptimeRobot at this bracket

## Value for 100 Monitors

| Service | Price/Month | Interval | Notable Features |
|---------|-------------|----------|------------------|
| **BetterStack** | $269 | 30 seconds | 6 users, unlimited alerts, on-call |
| **UptimeRobot** | ~$29-34 | 1 minute | 100 monitors included |
| **Pingdom** | ~$50-60 | 1 minute | Transaction monitoring available |
| **Pulsetic** | $29 ($19 + 50 × $0.20) | 30 seconds | Add-on monitors are linear and uncapped |

**Winner**: UptimeRobot (best price/monitor ratio); Pulsetic is close and buys a faster interval

> **Read the `+` signs.** Every Pulsetic plan is a floor, not a bundle: monitors are $0.20/mo
> each, SMS/calls $0.10, teammates $8, status-page subscribers $0.01. 500 monitors on
> Organization is $49 + 200 × $0.20 = **$89/mo**. See [../pulsetic.md](../pulsetic.md).
