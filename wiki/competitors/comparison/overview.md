# Uptime Monitoring Comparison — Overview & Market Moves

## Quick Overview

| Feature | BetterStack Uptime | Hyperping | UptimeRobot | Pingdom | StatusCake | Checkly | Healthchecks.io |
|---------|-------------------|-----------|-------------|---------|------------|---------|-----------------|
| **Founded** | 2021 (as Better Uptime) | 2018 | 2010 | 2007 | 2010 | 2018 | 2015 |
| **Owner** | Independent | Independent (bootstrapped) | Independent | SolarWinds | Accel-KKR | Independent (VC) | Independent |
| **Primary Market** | Modern DevOps teams | SMB / mid-market | Budget-conscious users | Enterprise | Mid-market | Developer teams | Cron/heartbeat |
| **Pricing Model** | Modular/component | Tiered (per-feature) | Monitor-based | Monitor + feature | Monitor-based | Usage-based (runs) | Check-based |
| **Best Known For** | Incident management | Outage-vs-incident split, multi-region confirm | 50 free monitors | RUM + Enterprise | Broad protocol support | Monitoring as code | Cron monitoring |
| **Self-hosted** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ (BSD) |
| **API Version** | v2 (v3 for incidents) | mixed v1/v2/v3 per resource | v2 (v3 available) | 3.1 (2.1 legacy) | v1 | v1 | v3 |

> **Also analyzed**: [Site24x7](../site24x7.md) (Zoho/ManageEngine) — all-in-one mid-market alternative with 100+ monitor types, 130+ probe locations, 50-monitor free tier, and built-in APM/RUM. Not included in tables below to keep them focused on uptime-first competitors.

> **Closest self-hosted analogue**: [Maintenant](../maintenant.md) (kOlapsis) — self-hosted Go single-binary like SolidPing, with deep Docker/Kubernetes container observability, HTTP/TCP/SSL/heartbeat checks, an MCP server for AI assistants, and an AGPL-3.0 open-core (Community/Pro/Enterprise) model. SQLite-only, ~17 MB RAM, four core probe types, and no built-in auth (reverse-proxy gated). Not in the tables below (kept SaaS-first), but its container monitoring and Docker-label config are the standout features worth studying.

> **Where SolidPing stands today (counts refreshed 2026-07-22)**: 40 check types (39 registered internally, including a non-customer-facing sleep/keepalive type — broadest of any tool surveyed either way), 10 native notification channels, multi-region distributed workers with per-region check periods, private locations (deported agents the server cannot decrypt secrets for), status pages with availability, subscriber notifications, and maintenance windows, **adaptive incident resolution + group-incident correlation + ack/snooze/manual-resolve**, **on-call schedules + multi-step escalation policies**, **credentials encryption at rest** (envelope encryption with out-of-band master key), configuration as code (YAML export/import/apply via API + CLI), labels with autocomplete + filtering, 2FA, MCP/AI integration, browser monitoring (chromedp), Prometheus metrics, dual PostgreSQL/SQLite backend, single-binary self-hosting. See [solidping-position.md](solidping-position.md) for the full ✅/❌ inventory.

> **Design ideas worth borrowing**: distilled in [../../research/alerting-patterns.md](../../research/alerting-patterns.md) — synthesizes findings from the deep-dive research on BetterStack ([../betterstack/](../betterstack/)) and Hyperping ([../hyperping/](../hyperping/)) into actionable input for future specs.

## 2026-07 refresh — pricing, releases & market moves

Re-verified against live sources on **2026-07-12**. The detailed tables below are
the May-2026 baseline; this block records what changed since. Indie/OSS long-tail
deltas live in [indie-watch.md](../indie-watch.md).

**Market moves**
- **Freshping (Freshworks) shut down on 2026-03-06** (data deleted 90 days after).
  A free-tier incumbent has left the market → a concrete migration/displacement
  opportunity for both SolidPing self-host and any hosted tier. No other
  acquisitions or major shakeups surfaced for Better Stack / Pingdom / UptimeRobot
  in 2026.

**OSS self-hosted rivals**
- **Uptime Kuma** — now on the **2.x stable line** (v2.4.0, 2026-05-31), **~89k★**.
  The top features driving Kuma users to alternatives — REST API, distributed
  multi-location probing, multi-user/SSO, PostgreSQL — are exactly SolidPing's
  differentiators.
- **Gatus** — **~11.5k★**; feature set steady (HTTP/TCP/DNS/ICMP, OIDC, 40+
  alerters). Latest release number unverified.

**SaaS pricing (confirmed / changed vs May-2026)**
- **UptimeRobot** — unchanged: **50 free monitors @5-min**; entry Solo **€7/mo**
  (annual) / €8 monthly, 10 monitors @60-sec. Newly flagged: API monitoring, UDP,
  multi-location. Pricing now shown in €.
- **Better Stack** — entry monitors add-on ~**$25/mo** ($21 yearly); free tier now
  advertised as *"up to 30-sec"* checks and on-call described as bundled rather
  than a separate ~$34 line. ⚠️ *Both the free-tier interval and whether on-call is
  genuinely free-vs-repackaged are "up to"-marketing and remain **unverified** —
  confirm on the pricing page before writing either as fact.*
- **StatusCake** — unchanged free (10 monitors @5-min); entry **Superior €16.66/mo**
  (annual) / €19.99 monthly, 100 monitors @1-min, 9 seats. Legal entity footer now
  **"TrafficCake Limited"** (rebrand of the entity name, not the product).
- **Checkly** — run-based model intact (free **1,000 browser + 10,000 API runs/mo**);
  now also surfaces explicit uptime-monitor counts (**10 free / 50 Starter**);
  Starter **$24/mo** annual, overage $6.50/1k browser + $2.60/10k API. Canonical
  domain is **checklyhq.com** (checkly.com/pricing 404s). No AI/agentic feature
  confirmed on the pricing page (unverified).
- **Healthchecks.io** — unchanged: 20 free checks; Business **$20/mo** (100 checks).
- **Pingdom** — unchanged: no free tier (trial only), SolarWinds-owned; entry price
  calculator-gated (~$15/mo baseline, exact current figure unverified).
- **Site24x7** — unchanged: Free-Forever tier + **$9/mo Starter** (10 sites, 1
  synthetic transaction).

*Sources: each vendor's live pricing page + GitHub repos, fetched 2026-07-12.
Items marked "unverified" could not be confirmed from a primary page and must not
be treated as fact without a manual check.*
