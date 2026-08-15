# SolidPing Competitive Position

## SolidPing Advantages Over All Three

| Advantage | vs BetterStack | vs UptimeRobot | vs Pingdom |
|-----------|----------------|----------------|------------|
| **Self-hosted** | ✅ vs SaaS | ✅ vs SaaS | ✅ vs SaaS |
| **No vendor lock-in** | ✅ | ✅ | ✅ |
| **Unlimited monitors** | ✅ (paid limits) | ✅ (plan limits) | ✅ (expensive) |
| **No recurring costs** | ✅ ($18-269/mo) | ✅ ($7-64/mo) | ✅ ($10-120+/mo) |
| **Privacy-first** | ✅ | ✅ | ✅ |
| **Direct DB access** | ✅ | ✅ | ✅ |
| **Open source potential** | ✅ | ✅ | ✅ |
| **PostgreSQL-native** | ✅ | ✅ | ✅ |
| **No false positives** | ✅ (control infra) | ✅ (control infra) | ✅ (known issue) |
| **Customizable** | ✅ | ✅ | ✅ |

## Features SolidPing Should Prioritize

Based on competitive analysis, prioritize these features:

**Tier 1 - Critical for Parity** (done):
1. ✅ HTTP/HTTPS monitoring (with JSON body validation, regex, custom UA)
2. ✅ Heartbeat/cron monitoring
3. ✅ Keyword monitoring (string + regex matching)
4. ✅ TCP / UDP port monitoring
5. ✅ Ping/ICMP monitoring
6. ✅ SSL certificate expiration alerts
7. ✅ SMTP / POP3 / IMAP monitoring
8. ✅ SSH, FTP, SFTP monitoring
9. ✅ WebSocket, gRPC monitoring
10. ✅ DNS monitoring (A, AAAA, CNAME, MX, NS, TXT)
11. ✅ Domain expiration monitoring (RDAP-first, WHOIS fallback)
12. ✅ Database monitoring (Postgres, MySQL, MSSQL, Oracle, MongoDB, Redis)
13. ✅ Message-queue monitoring (Kafka, RabbitMQ, MQTT)
14. ✅ Docker container, SNMP, A2S/Minecraft game server, custom JS check, browser (Rod) monitoring
15. ✅ Multiple notification channels — 10 native: Slack (OAuth + threads + Marketplace install), Discord (OAuth + webhook), Email, Webhooks, Google Chat, Mattermost, Ntfy, Opsgenie, Pushover, Web Push
16. ✅ Public status pages with sections, resources, availability metrics, locale-aware date formatting
17. ✅ Multi-location checking (distributed workers + multi-region)
18. ✅ Monitor grouping (check groups + group-incident correlation)
19. ✅ Advanced HTTP options (custom headers, body, methods, custom user-agent)
20. ✅ Response time tracking (min/max/avg metrics, period-based aggregation, configurable retention)
21. ✅ Incident management — adaptive resolution, group-incident correlation, ack/snooze/manual-resolve
22. ✅ On-call schedules (rotations + overrides) and multi-step escalation policies
23. ✅ Audit logging / events system
24. ✅ Maintenance windows (with recurrence)
25. ✅ JSON body validation / JSONPath queries
26. ✅ 2FA / MFA (TOTP)
27. ✅ Credentials encryption at rest (envelope encryption with out-of-band master key)
28. ✅ Prometheus `/metrics` endpoint
29. ✅ Sentry integration
30. ✅ MCP (Model Context Protocol) for AI/LLM access
31. ✅ Check import/export (JSON), check clone, check templates
32. ✅ Real-time check validation, sample configs, type registry
33. ✅ Internationalization (i18n) — English + French
34. ✅ Personal Access Tokens, OAuth (Google, GitHub, GitLab, Microsoft, Slack, Discord) with per-provider enable toggle
35. ✅ Status badges (SVG)
36. ✅ Labels with autocomplete API and list-page filtering
37. ✅ Email inbox passive monitoring via JMAP (deliverability end-to-end)
38. ✅ Status-page subscriber notifications — end users subscribe to a status page and are emailed on published status updates/incidents (wired end-to-end, not just a subscription list)
39. ✅ Configuration as Code — declarative YAML export/import/apply via `POST /orgs/:org/checks/apply` and the `sp` CLI (`export`/`import`/`apply`)
40. ✅ Per-org check-execution rate limiting + cost/plan-weighted scheduler fairness — a token-bucket `maxChecksPerMinute` entitlement plus scheduler-level de-prioritization of slow checks under contention; addresses "one tenant can't DoS the shared workers" via a different mechanism than the original proportional-fair-period-scaling design

**Tier 2 - High-Impact Gaps** (not yet implemented, multiple competitors offer these):
1. ❌ Telegram, Microsoft Teams, PagerDuty notification channels — specs ready in `specs/ideas/2026-03-22-telegram-notifications.md` and `specs/ideas/2026-03-22-notification-channels.md`
2. ❌ Screenshot capture on HTTP failure (BetterStack, Checkly) — research done, Rod chosen, spec ready in `specs/ideas/2026-01-05-screenshots.md`
3. ❌ Importers from BetterStack / UptimeRobot / Uptime Kuma (spec stub in `specs/ideas/2025-12-28-importers.md` — lowers switching friction)
4. ⚠️ Terraform provider (Gatus, Checkly, BetterStack) — lives in a separate `terraform-provider-solidping` repo per a "done" spec; this repo only has an API-completeness audit (`../../terraform-provider-api-audit.md`), so its actual shipped/published state isn't verifiable from here

**Tier 3 - Competitive Differentiators** (nice to have):
1. ❌ Page speed / Core Web Vitals monitoring (Pingdom, StatusCake)
2. ❌ Real User Monitoring / RUM (Pingdom, Site24x7)
3. ❌ Traceroute/MTR diagnostics on failure (BetterStack)
4. ❌ Mobile applications (UptimeRobot, Pingdom) or installable PWA
5. ❌ GitHub/GitLab issue integration (Gatus)
6. ❌ SMS / Voice escalations (every major SaaS via Twilio)
7. ❌ Heartbeat enhancements — `/start` endpoint, exit codes, log attachment (Healthchecks.io)
8. ❌ Automatic application discovery — suggest healthcheck endpoints from URL (spec in `specs/ideas/2025-12-28-automatic-app-discovery.md` — no competitor has this)
9. ❌ AIOps / anomaly detection on response-time series (Site24x7, Datadog)
10. ❌ Subchecks (parent HTTP check auto-spawns SSL/domain-expiration sub-checks — spec stub in `specs/ideas/2026-01-01-subchecks.md`)

## SolidPing Unique Strengths (no single competitor matches all)

| Strength | Closest Competitor |
|----------|-------------------|
| Self-hosted + Multi-tenancy + RBAC + 2FA | None (unique combination) |
| 38 check types in a single binary | Site24x7 (SaaS only); Uptime Kuma has ~12 |
| Dual PostgreSQL / SQLite + embedded Postgres | None (most OSS tools are single-DB) |
| Distributed workers + multi-region scheduling | SaaS only (BetterStack, Pingdom); not in self-hosted OSS |
| Group-incident correlation (one alert per outage, not per check) in self-hosted | BetterStack (SaaS only, "automatic incident merging") |
| Incident management with adaptive resolution + ack/snooze/manual-resolve in self-hosted | BetterStack (SaaS only) |
| On-call schedules + multi-step escalation policies in self-hosted | Opsgenie / PagerDuty (paid SaaS); BetterStack (SaaS only) |
| Credentials encryption at rest with envelope encryption | None in self-hosted category |
| Maintenance windows with recurrence in self-hosted | BetterStack, UptimeRobot (SaaS only) |
| Browser checks (chromedp) self-hosted | Checkly, BetterStack (SaaS only) |
| Email inbox passive monitoring via JMAP (deliverability) | None |
| MCP server for AI/LLM tool integration | None |
| Sandboxed JavaScript checks (no external runtime) | Gatus (external script only) |
| Full audit logging / events system + Prometheus `/metrics` | BetterStack (SaaS), Gatus (metrics only) |
| OAuth multi-provider auth (Google, GitHub, GitLab, Microsoft, Slack, Discord) with per-provider toggle | None in self-hosted category |
| Slack OAuth + threaded incident messages + Marketplace direct install | BetterStack (SaaS only) |
| Labels with autocomplete + filtering + check clone + check templates | Partial in BetterStack (tags); no self-hosted match |
| Private locations (deported agents) where the server **cannot decrypt** check secrets | None — Checkly / Datadog / New Relic / Grafana / Site24x7 all have vendor-readable secrets; SolarWinds forbids secrets on private probes. See [features/deported-agents.md](../../features/deported-agents.md#competitive-position) |

## Pricing Strategy Recommendation

**SolidPing SaaS Pricing** (if offered):

| Tier | Price | Monitors | Interval | Strategy |
|------|-------|----------|----------|----------|
| **Free** | $0 | 50-100 | 5 minutes | Beat UptimeRobot, crush Pingdom |
| **Starter** | $5 | 50 | 1 minute | Undercut all three |
| **Pro** | $15 | 100 | 30 seconds | 50% cheaper than BetterStack |
| **Business** | $49 | 500 | 30 seconds | Volume pricing advantage |

**Self-hosted**: Always free, unlimited monitors (main differentiator)
