# Gatus — Competitor Analysis

Gatus is an advanced, developer-oriented health dashboard and status page system created by TwinProduction (Chris Gervais). It's positioned as "the most advanced status page in the world" with a focus on configuration-as-code, lightweight architecture, and powerful condition-based monitoring.

This directory replaces the previous monolithic `gatus.md` (879 lines) and re-organizes the material per the size rule in `../../CLAUDE.md`.

**GitHub**: https://github.com/TwiN/gatus · **Website**: https://gatus.io · **License**: Apache 2.0 · **Technology**: Go (backend), simple HTML/CSS/JS (frontend) · **Database**: SQLite or PostgreSQL (optional) · **Current Version**: v5.34.0 (as of March 2026)

## Files in this directory

- [configuration.md](configuration.md) — Configuration as code (YAML), endpoint structure, condition placeholders, technology stack.
- [deployment.md](deployment.md) — Installation (Docker, binary, source, Kubernetes), performance & scalability, security (auth, TLS, secrets).
- [api.md](api.md) — REST API surface, response format, status / health / metrics endpoints.
- [alerting.md](alerting.md) — Alert configuration, thresholds, provider-specific examples (Slack, GitHub, SMTP).
- [comparison.md](comparison.md) — Strengths, weaknesses, comparison with SolidPing, feature gaps, use cases, migration & integration, community & support.
- [sources.md](sources.md) — All source URLs used.

## At a glance

| Aspect | Gatus |
|---|---|
| Founded | 2019 (active development since) |
| License | Apache 2.0 |
| GitHub Stars | 10,100+ (Mar 2026) |
| Forks | 400+ |
| Releases | 100+ versions |
| Contributors | 50+ community members |
| Written in | 100% Go |
| Database | In-memory · SQLite · PostgreSQL |
| Endpoint types | 9 (HTTP/HTTPS, TCP, ICMP, DNS, WebSocket, SSH, TLS, STARTTLS, External) |
| Configuration | YAML only (no UI) |
| Alert providers | 20+ (Slack, Discord, Teams, PagerDuty, Opsgenie, Email, Twilio, GitHub, GitLab, Gitea, Matrix, Mattermost, Pushover, Ntfy, Home Assistant, AWS SES, custom webhooks, Telegram, Zulip, …) |
| Observability | Prometheus `/metrics`, Grafana dashboards, health endpoint |
| Auth | Basic auth · OIDC · API keys · TLS |
| Notable | Configuration-as-code · powerful condition syntax with JSONPath · stateless option · GitOps-friendly · auto-create/close GitHub issues · lightweight (< 20 MB binary) |

## Headline takeaways

**Strengths**: configuration-as-code (YAML, Git-friendly) · stateless option · Go performance · 20+ alert providers · powerful condition syntax with JSONPath, `len()`, `has()`, `pat()` · TLS / domain expiration tracking · GitHub/GitLab issue auto-create/close · Prometheus metrics built-in · Kubernetes-ready (Helm charts).

**Weaknesses**: no UI configuration (YAML only) · single status page · no Docker / database connection monitoring · no built-in heartbeat/cron · no multi-user / RBAC · read-only API · in-memory by default (data lost on restart unless configured).

**For SolidPing**: Gatus demonstrates that configuration-as-code is highly valuable for DevOps teams. SolidPing should consider YAML-based configuration alongside its UI, powerful condition syntax with JSONPath, stateless mode, Prometheus metrics, and GitHub/GitLab issue integration — while preserving the UI-first, multi-tenant, heartbeat-capable strengths it already has.
