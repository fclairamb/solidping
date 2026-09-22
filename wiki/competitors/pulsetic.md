# Pulsetic - Complete Analysis

## Overview

Pulsetic is a hosted uptime-monitoring service built around four pillars: synthetic
uptime checks, brandable status pages, Real User Monitoring, and **dependency
monitoring** — a catalogue of 4,300+ third-party providers whose own status pages
Pulsetic watches on your behalf. It sits in the same indie-SaaS bracket as Hyperping
and the cheaper Better Stack tiers: cheap entry price, polished status pages, no
self-hosting.

Two things make it worth a deep-dive rather than a line in `full-list.md`:

1. **Dependency monitoring is a capability SolidPing does not have at all.** Every
   other gap in this file runs the other way.
2. **It ships a first-party MCP server**, which makes it the only competitor
   surveyed that competes with SolidPing on AI-assistant integration.

**Website**: https://pulsetic.com

**Owner**: Designmodo Inc. (per the site footer) — the design-tooling company behind
Slides, Postcards and Startup. Founder Andrian Valeanu. *Launch year commonly cited
as 2022; not confirmed from a primary source — treat as unverified.*

**API base URL**: `https://api.pulsetic.com/api/public`

**Self-hosted**: ❌ (SaaS only)

## Key Features

### Monitor Types

Marketed as thirteen named products, which map onto a much smaller set of actual
probes:

| Pulsetic name | What it really is |
|---|---|
| Website / API / Keyword monitoring | HTTP(S) check, with body-string matching |
| Ping monitoring | ICMP |
| Port / TCP monitoring | TCP connect |
| SSL monitoring | Certificate expiry & validity |
| Domain monitoring | Domain expiry |
| Cron monitoring | Passive heartbeat (inbound ping) |
| Monitor screenshots | Screenshot capture attached to a check result |
| SLA monitoring | Reporting layer over uptime data, not a probe |
| Real User Monitoring | Browser JS beacon (see below) |
| Dependency monitoring | Third-party status-page ingestion (see below) |

So the genuine protocol surface is **HTTP, ICMP, TCP, SSL, domain and heartbeat** —
six probe families. No DNS, SMTP/IMAP/POP3, SSH, FTP/SFTP, gRPC, WebSocket, UDP,
database, message-queue, SNMP or game-server checks, and no scripted browser
transactions (the screenshots are captures, not Playwright flows).

### Dependency monitoring — the genuinely differentiated feature

Pick a provider out of a **4,300+ service catalogue** (payments, cloud, developer
tools, AI APIs, comms, SaaS apps), add it with one click, and Pulsetic relays that
provider's own incidents to you: opened, updated, resolved. Their framing, from the
feature page: *"Before your team digs through its own code, the alert says whether
Stripe, SendGrid or GitHub has already declared an incident."*

This is not synthetic monitoring at all — it is status-page aggregation — but it
answers a real on-call question ("is it us or is it them?") that no amount of probe
breadth answers. Alerts are **email-only** on this monitor type.

**SolidPing has no equivalent.** The 40 check types are all active probes; nothing
consumes another vendor's status page. Worth noting that Uptime Kuma has an open
feature request for exactly this shape of thing
([#7688](https://github.com/louislam/uptime-kuma/issues/7688)), so demand is
expressed against the OSS market leader too.

### Real User Monitoring

A JS snippet in `<head>`, no build change. Captures:

- Core Web Vitals (LCP, INP, CLS) plus FCP, at the 75th percentile
- Page-load breakdown (DNS, connect, TTFB, full load) at p50/p75/p95
- Uncaught JS exceptions with stack traces, and an errors-per-100-views rate
- Failed `fetch`/XHR by status code
- Live visitor counts and referrers

Sliced by page, country, city, browser, device and referrer. The page claims no
cookies and asynchronous loading. RUM is metered by **page views per month**, not by
monitor count.

SolidPing has no RUM. So does Pingdom, and Site24x7 — this is the standard "we're
not only synthetic" upsell in this market.

### Monitoring Configuration

**Check intervals and region fan-out by plan**:

| Plan | Interval | Regions per check |
|---|---|---|
| Free | 5 min | 3 |
| Solo | 60 sec | 5 |
| Team | 30 sec | 15 |
| Organization | 30 sec | 15 |

Failures are confirmed from multiple regions before an alert fires — the same
false-positive guard Hyperping markets, and the same one SolidPing implements with
its multi-region workers.

### Probe Locations

**15 locations across 5 continents.** Pulsetic does not publish the city list; the
knowledge base points at IP ranges instead:

- `https://api.pulsetic.com/ip-ranges.txt` (IPv4)
- `https://api.pulsetic.com/ipv6-ranges.txt` (IPv6)
- `dig nodes.pulsetic.com A +short` — **41 A records** as of 2026-09-22

Nodes are a fixed, vendor-operated fleet. **No private locations / customer-hosted
agents** — the boundary where SolidPing's deported agents win outright.

### Notification Channels

Email, SMS, phone call, webhook, Slack, Discord, Microsoft Teams, Telegram,
Mattermost, SIGNL4; Zapier, n8n and Cloudflare listed as integrations. The
integrations page carries a *"More integrations are on the way"* caveat, so the list
is in motion.

Roughly **ten channels vs SolidPing's seventeen** (SolidPing adds Google Chat, ntfy,
Gotify, Matrix, Zulip, PagerDuty, Pushover, Web Push and WhatsApp; Pulsetic adds
SIGNL4, which SolidPing lacks).

⚠️ **On-call schedules and multi-step escalation policies are unverified.** Pulsetic
publishes a *glossary* page explaining what an escalation policy is, and there is a
third-party AlertOps integration, but no product page or knowledge-base article
confirming first-party rotations/escalation was found. Do not claim either way in
copy without a logged-in check.

### Status Pages

The strongest part of the product, and clearly the reason most reviews are positive:

- Public, password-protected, or SSO-gated
- Custom domain (`status.example.com`) with custom email sender
- Logo, colours, fonts; multi-language translations
- Subscriber notifications, incident management, scheduled maintenance
- PDF and image uptime-report exports
- Live uptime badges

**White-label / agency model**: one Pulsetic account manages every client, each with
its own branded page and custom domain; clients never get a login. Explicitly
marketed at agencies and hosting providers.

## Pricing

Verified on the live pricing page, 2026-09-22.

| | Free | Solo | Team | Organization |
|---|---|---|---|---|
| Monthly | $0 | $9 | $19 | $49 |
| Annual | $0 | $90 | $190 | $490 |
| Monitors / heartbeats / domains | 10 | 10+ | 50+ | 300+ |
| Check interval | 5 min | 60 sec | 30 sec | 30 sec |
| Regions per check | 3 | 5 | 15 | 15 |
| Teammates | — | 0+ | 2+ | 3+ |
| SMS / call alerts included | — | 30+ | 100+ | 200+ |
| Webhooks per monitor | 1 | 1 | 5 | 10 |
| Status pages | — | 3 | Unlimited | Unlimited |
| Status-page subscribers | — | 1,000+ | 5,000+ | 10,000+ |
| RUM page views / month | 1,000 | 10,000+ | 100,000+ | 200,000+ |
| RUM analytics retention | 3 months | 1 year | 1 year | 5 years |
| Error-log retention | 30 days | 90 days | 6 months | 1 year |

Annual billing is "save 2 months" (10× monthly).

**Add-ons, metered monthly** — this is how the bill actually grows:

| Add-on | Price |
|---|---|
| Extra monitor | $0.20 |
| Extra SMS / call alert | $0.10 |
| Extra teammate | $8 |
| Extra status-page subscriber | $0.01 |

The `+` suffixes in the table are the tell: every plan is a floor, and the linear
add-on rates are the real pricing model. 100 monitors on Team = $19 + 50 × $0.20 =
**$29/mo**. 500 monitors on Organization = $49 + 200 × $0.20 = **$89/mo**.

**Free tier**: 10 monitors at 5-min from 3 regions, unlimited email alerts, RUM
capped at 1,000 page views — but **no status pages at all**, which is unusual given
status pages are the product's centre of gravity. Compare UptimeRobot's 50 free
monitors and StatusCake's 10-with-a-status-page.

⚠️ Pulsetic has historically run AppSumo lifetime deals and "-50% lifetime"
promotions. A meaningful share of its user base may be on LTD terms rather than
these list prices — relevant if you ever model their revenue or churn.

## API

### Design

REST over HTTPS, JSON in and out. Auth is a single API token created under
*Settings → API* and sent in the `Authorization` header — their own wording:
*"No SDKs to install and no OAuth dance: a token and an HTTP client are all you
need."*

- **Base URL**: `https://api.pulsetic.com/api/public`
- **No version segment in the path** — no `/v1`. Breaking changes have nowhere to go.
- **OpenAPI**: an `api.yaml` is published for import into Swagger Editor
- **SDKs**: none official
- **Rate limit**: `monitor limit × 3 req/min`, capped at 7,000 req/min — a limit
  that scales with your plan, which is a neat idea worth stealing
- **Plan-gated**: API access is **Team and Organization only**. Free and Solo have
  no API at all.

### Endpoint Coverage

| Resource | Coverage |
|---|---|
| Monitors | Full CRUD, plus `/snapshots`, `/checks`, `/events`, `/stats`, `/downtime` |
| Heartbeats | Full CRUD |
| Domains | Full CRUD |
| Status pages | Full CRUD |
| Maintenance | Create / update / delete under a status page |
| Incidents | Full CRUD under a status page |
| Incident updates | Full CRUD |
| Notification channels | List, delete, and one POST per channel type (`/email`, `/phone-number`, `/webhook`, `/slack-webhook`, `/discord-webhook`, `/ms-teams-webhook`, `/signl4`) |

Note the channel design: **a separate endpoint per channel type** rather than one
polymorphic resource. Adding a channel means adding an endpoint. SolidPing's
integration model is one resource with a type discriminator; that ages better.

Pagination is `page` / `per_page`. No configuration-as-code equivalent to SolidPing's
YAML export/import/apply.

### MCP Server

[`designmodo/pulsetic-mcp`](https://github.com/designmodo/pulsetic-mcp) — MIT,
created **2026-03-02**, last pushed 2026-09-18, **2 stars**. Wraps the public API so
Claude, ChatGPT, Cursor and Windsurf can read uptime data and manage monitors.

This is the only competitor in this wiki with a first-party MCP server. SolidPing
also ships MCP/AI integration, so the differentiation here is *parity, not lead* —
worth keeping an eye on, because "manage your monitoring in plain English" is a
marketing angle both sides can now claim.

## Strengths

- **Dependency monitoring at 4,300+ providers** — no one else surveyed does this, and
  it answers the first question of any incident
- **Status pages are genuinely good**: custom domain, SSO gating, translations, PDF
  exports, badges, subscribers — and unlimited on Team at $19
- **Coherent white-label/agency story**: one account, many branded client pages, no
  per-client logins
- **RUM bundled**, not a separate SKU — Core Web Vitals plus JS error tracking
- **Cheap and legible**: $9/$19/$49 with published per-unit add-on rates, no "contact
  sales"
- **Rate limit scales with plan** rather than a flat number
- **First-party MCP server**, shipped and maintained

## Weaknesses

- **Six probe families against SolidPing's 40 check types.** No DNS, SMTP, IMAP/POP3,
  SSH, FTP/SFTP, gRPC, WebSocket, UDP, database, queue, SNMP or game-server checks
- **No scripted browser transactions** — screenshots are captures, not flows
- **No self-hosting, no private locations.** Anything behind a VPN or inside a private
  subnet is out of reach
- **API is Team-and-up**, so the $9 plan is click-ops only
- **Unversioned API path** — no migration story
- **Free tier has zero status pages**, which undercuts the product's own headline
- **Add-on pricing is linear and uncapped**: monitors at $0.20 each and teammates at
  $8 each add up fast at scale
- **Owned by a design-tools company**, not a monitoring company — Pulsetic is one
  product in a portfolio, which is a concentration risk worth naming when a buyer asks
  about longevity (Freshping's 2026-03-06 shutdown is the cautionary case)
- **On-call / escalation unverified** — see the ⚠️ above

## Comparison with SolidPing

### Similarities

- Multi-region checks with cross-region confirmation before alerting
- Status pages with custom domains, subscribers, incidents and maintenance windows
- Heartbeat/cron monitoring
- Token-auth REST API with an OpenAPI schema
- First-party MCP server for AI assistants
- Webhook + chat-platform alerting

### Pulsetic Advantages

| Capability | Pulsetic | SolidPing |
|---|---|---|
| Dependency monitoring (3rd-party status pages) | ✅ 4,300+ services | ❌ |
| Real User Monitoring | ✅ CWV + JS errors | ❌ |
| Monitor screenshots | ✅ | ❌ |
| SIGNL4 alerting | ✅ | ❌ |
| Status-page PDF/image export | ✅ | ❌ (verify) |
| Zero-infra onboarding | ✅ hosted | ⚠️ self-host or SaaS |
| SSO-gated status pages | ✅ | ⚠️ verify parity |

### SolidPing Advantages

| Capability | SolidPing | Pulsetic |
|---|---|---|
| Check types | **40** | ~6 probe families |
| DNS / SMTP / IMAP / SSH / FTP / gRPC / WebSocket / UDP | ✅ | ❌ |
| Database checks (6 engines) | ✅ | ❌ |
| Message queues (Kafka / RabbitMQ / MQTT) | ✅ | ❌ |
| SNMP, game servers, email inbox (JMAP) | ✅ | ❌ |
| Browser checks (chromedp) | ✅ | ❌ |
| Sandboxed JS checks | ✅ | ❌ |
| Self-hosting (single binary) | ✅ | ❌ |
| Private locations / deported agents | ✅ | ❌ |
| On-call schedules + escalation policies | ✅ | ⚠️ unverified |
| Configuration as code (YAML + CLI) | ✅ | ❌ |
| Notification channels | 17 | ~10 |
| API on every plan | ✅ | ❌ (Team+) |
| Versioned API | ✅ `/api/v1` | ❌ unversioned |
| Credentials encrypted at rest | ✅ | n/a (SaaS) |

### What SolidPing Should Learn

1. **Dependency monitoring is the idea worth stealing.** A check type that subscribes
   to a provider's status page (most publish Atom/RSS or a statuspage.io JSON API) and
   raises a SolidPing incident is a small, self-contained feature with an obvious
   on-call payoff. It also composes with group-incident correlation: "your checks are
   red *and* your CDN has declared an incident" is one screen instead of two tabs.
2. **Rate limits that scale with the plan** (`monitors × 3/min`) beat a flat number —
   they stay proportionate without an ops conversation.
3. **Publish probe IPs as both a text file and a DNS name.** `dig nodes.pulsetic.com`
   is a better allowlisting UX than a docs page someone has to scrape.
4. **Uptime reports as PDF/image exports.** Cheap to build, and it is what agencies
   and anyone with an SLA actually hand to a client.
5. **Don't copy the free tier.** Withholding status pages from the free plan when
   status pages are your best feature is a self-inflicted wound.

## Use Cases

### Best For

- Agencies and hosting providers who need many branded client status pages from one
  account
- Small teams whose stack is HTTP-shaped and who want a status page in ten minutes
- Anyone who wants uptime + RUM + third-party incident awareness in one $19 bill

### Not Ideal For

- Anything behind a VPN, in a private subnet, or on a non-HTTP protocol
- Infrastructure monitoring (databases, queues, SNMP, SSH)
- Teams that want their monitoring in git
- Anyone who needs the API on a cheap plan

## Competitive Positioning

### vs SolidPing

Pulsetic wins the "I have a website and no infrastructure" buyer. SolidPing wins the
moment the stack has a database, a queue, a private network, an SSH host, or a
compliance requirement that the probes stay inside it. The honest one-liner:
**Pulsetic monitors the front door; SolidPing monitors the building.**

The counter-angle to have ready is that their breadth story is thirteen marketing
names over six probes, while our forty is a derivable count
(`grep -rhoE 'CheckType = "[a-z0-9_-]+"' server/internal/checkers/checkerdef/*.go`
minus `sleep`). Say it with the number, not with adjectives.

### vs Hyperping

Nearly the same buyer and the same "multi-region confirm before alerting" pitch.
Pulsetic adds RUM and dependency monitoring; Hyperping's outage-vs-incident split is
sharper. Pulsetic is cheaper at entry.

### vs UptimeRobot

UptimeRobot's 50-monitor free tier is far more generous and it has more protocols
(UDP, DNS, SMTP-adjacent). Pulsetic's status pages and RUM are better. Different
centres of gravity despite the same price bracket.

### vs Better Stack

Better Stack owns incident management and on-call; Pulsetic does not credibly compete
there (and may not have it at all). Pulsetic is several times cheaper.

## Sources

Fetched and verified 2026-09-22 unless noted.

### Official
- [Pulsetic homepage](https://pulsetic.com/)
- [Pricing](https://pulsetic.com/pricing/)
- [API for developers](https://pulsetic.com/api/)
- [API endpoints reference](https://help.pulsetic.com/article/260-api-endpoints)
- [Locations and IPs](https://help.pulsetic.com/article/195-locations)
- [Dependency monitoring](https://pulsetic.com/dependency-monitoring/)
- [Real User Monitoring](https://pulsetic.com/real-user-monitoring/)
- [Integrations](https://pulsetic.com/integrations/)
- [White-label monitoring](https://pulsetic.com/white-label-website-monitoring/)
- [`designmodo/pulsetic-mcp`](https://github.com/designmodo/pulsetic-mcp) (GitHub API, MIT, created 2026-03-02)

### Unverified / needs a logged-in check
- On-call schedules and multi-step escalation policies
- Status-page SSO implementation details
- Launch year (2022, secondhand)
- Current AppSumo / lifetime-deal terms

**Last Updated**: 2026-09-22
