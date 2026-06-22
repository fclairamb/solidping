# UptimeRobot — Competitor Analysis

UptimeRobot positions itself as "the world's leading uptime monitoring service" with over 2.5 million active users. The platform offers a generous free tier with 50 monitors and provides comprehensive monitoring capabilities through a modern REST API.

This directory replaces the previous monolithic `uptimerobot.md` and re-organizes the material per the size rule in `../../CLAUDE.md`.

## Files in this directory

- [api.md](api.md) — API architecture, authentication, rate limits, and core v3 endpoints (monitors, alert contacts, integrations, maintenance windows, status pages, user).
- [api-reference.md](api-reference.md) — Complete API reference tables (endpoint summary, monitor types, status values, alert contact types, HTTP methods, query parameters, status codes, rate-limit headers).
- [heartbeats.md](heartbeats.md) — Heartbeat monitoring in-depth: how it works, setup process (cron, wget, Task Scheduler, Python), use cases, important notes.
- [pricing.md](pricing.md) — Free / Solo / Team / Enterprise plans and pricing insights.
- [comparison.md](comparison.md) — Comparison with SolidPing, technical considerations, limitations and gaps, API design patterns to adopt/avoid.
- [examples.md](examples.md) — Integration examples (web service, keyword, port, heartbeat, Slack, status page).
- [sources.md](sources.md) — All source URLs used.

## At a glance

| Aspect | UptimeRobot |
|---|---|
| Base API URL | `https://api.uptimerobot.com/v3/` (v3 current, v2 legacy) |
| API Specification | RESTful JSON API with JWT authentication |
| Users | 2.5+ million |
| Free tier | 50 monitors, 5-min interval, 1 status page |
| Min interval | 30 s (Enterprise) / 1 min (Solo+) |
| Monitor types | HTTP, KEYWORD, PING, PORT, HEARTBEAT, SSL, DOMAIN, DNS |
| Notifications | 12+ channels (Email, SMS, Voice, Slack, Teams, Discord, Telegram, PagerDuty, Pushover, Pushbullet, Zapier, Webhooks) |
| Auth | Bearer token (account / monitor-specific / read-only) |
| Rate limits | 10 req/min free; `monitor_limit × 2 req/min` paid (max 5,000/min) |
| Mobile | Native iOS and Android apps |
| Pricing | Free / $7–8 / $29–34 / $54–64+ per month |

## Headline takeaways

UptimeRobot excels at:

- **Accessibility** — 50 free monitors and easy setup
- **Simplicity** — Clean UI and straightforward API
- **Reliability** — 2.5+ million users, proven track record
- **Integration breadth** — 12+ notification channels
- **Mobile experience** — Native iOS and Android apps

However, it's a closed-source SaaS platform with:

- Limited free tier intervals (5 minutes)
- Heartbeat monitoring requires paid plan
- Less flexibility than self-hosted solutions
- No advanced incident management features

For SolidPing, UptimeRobot serves as an excellent reference for:

- API design evolution (v2 to v3 migration)
- Generous free tier strategy
- Heartbeat monitoring implementation
- Multi-location checking approach
- Status page API design
- Integration management patterns

While SolidPing may not need UptimeRobot's scale, understanding its approach to user acquisition (generous free tier), API modernization (v2 to v3), and feature prioritization helps inform development decisions.
