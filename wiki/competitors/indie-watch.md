# Indie / OSS / Emerging Competitor Watch

The long tail of monitoring entrants surfaced by SolidPing's marketing-listening
pipeline (Hacker News, Mastodon, Lobsters, dev.to, GitHub issues/PRs, LowEndTalk,
Lemmy) that are **not** deep-dived in the per-competitor tier files. Most are solo
/ indie / hackathon / content-farm projects with little traction; a handful are
genuine rivals. This page is the catalog; the tier files
([comparison/](comparison/README.md), [full-list.md](full-list.md), and the
per-vendor folders) stay focused on established players.

**Legend** — `track: true` = scope overlaps SolidPing enough to watch actively;
`track: false` = adjacent, novelty, or low-threat, logged for awareness only.
"Surfaced" gives provenance; treat single-post self-promo as marketing, not
community demand.

> Provenance note: migrated from the SolidPing marketing repo
> (`solidping-marketing/memory/competitors.md`) 2026-07-11. That repo's YAML
> registry remains the operational source for the listening pipeline's
> competitor-name matching; this page is the canonical human-readable analysis.

---

## Refresh log

### 2026-07-12 — web re-verification of the tracked set

Facts re-checked against live pricing/product pages (sources inline in each
entry). Deltas since the 2026-05/06 capture:

- **Status Harbor — prices ~doubled.** Starter $5 → **$10/mo**, Pro $12 → **$24/mo**
  (Pro now 25 monitors / 3 Lighthouse agents / 6 seats). Free tier is 5-min and
  HTTP/TCP/UDP only (SSL/DNS gated behind Starter+).
- **exit1.dev now advertises an MCP server** (AI-assistant access — the same
  category as SolidPing's MCP surface) plus WebSocket + ICMP checks; free tier is
  10 monitors, paid scales to 1,000 monitors @15s. Exact cheap-tier € figure
  unverified on the current site.
- **OpenStatus added a Scale tier at $500/mo** above Pro, and raised monitor
  allotments ("same pricing, more monitors"). Starter $30 / Pro $100 headline
  unchanged; free interval 10-min, Starter 1-min, Pro 30-sec.
- **failover.io** added an **ntfy** channel (now 10 channels) and **blockchain /
  RPC node monitoring with stall detection**; Free/$19 Pro/$79 Team prices
  unchanged.
- **HarborScale pivoted** from "monitor everything" uptime toward a telemetry /
  observability platform (Linux + Docker + ESP32/IoT metrics, managed Grafana
  built-in) — lower direct-uptime overlap now.
- **Peekaping** stable at v0.0.46 (2026-04-10), ~1.1k★, MIT, ~14 monitor types /
  20+ alert channels — momentum roughly flat. *(2026-07-26: still v0.0.46 — the
  flat momentum is now a ~3.5-month release gap; see the per-vendor entry.)*
- **OneUptime** pricing captured: Free / **Growth $22/mo** / **Scale $99/mo**
  (SSO) / Enterprise, plus PAYG **$1/active-monitor/mo**.
- **Updu** confirmed live (single Go binary, ~19 monitor types, GitOps config).
- **SolidUptime — no live product found; presumed inactive/parked.** The
  name-collision concern with "solidping" is therefore lower priority for now.
- **StatusDrift** still Cloudflare-blocks its pricing page (paid tiers
  unverified); positioning has shifted toward DevOps/SRE (Terraform provider,
  SLO/error-budget/burn-rate, on-call) rather than ITSM.
- **PulsorUp** live (blog active); pricing unverified.
- New self-hosted entrants worth watching added below: **EasyMonitor**,
  **Tianji**, **Kener** (see the new-entrants block at the end of the tracked
  section). Context: Uptime Kuma is now ~89k★ and the top features pushing its
  users to alternatives — REST API, distributed multi-location probing,
  multi-user/SSO, PostgreSQL — are exactly SolidPing's differentiators.

---

## Tracked closely (`track: true`)

Scope overlaps SolidPing's uptime + self-host + on-call story enough that a shift
in their roadmap would matter.

### Peekaping — https://peekaping.com  ·  github.com/0xfurai/peekaping
Self-hosted OSS uptime monitor, Go + React, MIT, **1,139★** (created 2025-06,
active through 2026-05). Markets verbatim as "the best uptime kuma alternative"
for professional DevOps teams. Websites/APIs, status pages, alerts; storage on
SQLite / PostgreSQL / MongoDB. Homepage + live demo + docs + community Terraform
provider; self-described beta. **The closest kind of rival — same lane as Uptime
Kuma itself.** SolidPing counters on 38 check types + distributed multi-region
workers + built-in on-call, none of which Peekaping matches yet; Peekaping's edge
is 1.1k★ of momentum, a polished site/demo, and pluggable storage backends.
*Watch:* whether it ships distributed probing or a hosted/paid tier. Surfaced
2026-07-02 via @selfhosted_bot trending repost (bot-announced → intel, not a lead).

**Update 2026-07-26 — release cadence has stalled.** Verified against the GitHub
API on 2026-07-26:

| Fact | Value |
|---|---|
| Last release | **0.0.46, 2026-04-10** (0.0.45 was 2025-12-21) |
| Commits since | 2 chores on **2026-05-24** (`robots.txt`, `.tool-versions.template`) |
| Last push (any branch) | 2026-05-24 |
| Open issues | **90**, incl. unanswered bug reports (#272, zombie processes on Tang endpoints, open since 2026-06-03, 0 replies) |
| Stars | 1,139 → **1,157** (still climbing) |

So: ~3.5 months without a functional release, ~2 months without any commit, while
inbound interest keeps growing. Caveats before anyone leans on this — it is a
single-maintainer project, a quiet stretch is not a shutdown, and there are ~10
open feature branches (`feat/incidents`, `feature/ha`, `feat/teams`, …) that may
mean work is happening off the default branch without being pushed. Re-verify the
release feed before treating this as durable.

**Re-verified 2026-08-01 — unchanged.** Still 0.0.46 (2026-04-10), last push
still 2026-05-24, open issues 90, stars 1,157 → **1,165**. The gap is now ~3.7
months without a release and ~2.3 months without a commit, with inbound interest
still growing. The 2026-07-26 condition for acting on this was "if the gap holds
for ~a month"; six days is not that. `track: true` retained, still not used in
copy — a solo maintainer's quiet stretch is not a shutdown. Re-check next poll.

### Kuvasz — https://kuvasz-uptime.dev  ·  github.com/kuvasz-uptime/kuvasz
Self-hosted OSS uptime **and SSL** monitor, Kotlin, AGPL-3.0, **571★** / 37 forks,
created 2020-07 and still shipping (v4.0.1 2026-06-17, v4.1.0 2026-07-14,
**v4.2.0 2026-08-10**, repo pushed 2026-08-12). A mature rival the listening
pipeline had simply never surfaced before 2026-07-28 — it predates most of this page.
**Ships:** HTTP monitoring (adjustable intervals, custom headers, keyword matching,
expected status codes, response-time thresholds); daily SSL-certificate expiry
checks; push/"cron" heartbeat monitors; ICMP ping monitors; **TCP monitors** and
**DNS monitors** (both added in v4.2.0) — **6 check types** (was 4 before
2026-08-10).
Per-monitor notification routing across email / Slack / Discord / Telegram /
PagerDuty / custom webhooks. Public **and private** brandable status pages,
maintenance windows, full REST API, monitors declared as YAML (IaC), **Prometheus
and OTLP** metric exporters, official Home Assistant integration, live demo.
**Ships an MCP server** (built in since v4.0.0; experimental, disabled by default):
query monitor status, view incidents, create/toggle monitors from Claude, Cursor,
or any MCP client.
**Significance — this is the entry that changes a positioning line.** On 2026-07-21
(UptimeMonitoring.com) the "AI-native" claim was narrowed to *self-hosted* MCP on
the reasoning that every MCP-shipping rival was SaaS. Kuvasz is a 571★ AGPL
self-hosted monitor with an MCP server, so the qualifier no longer separates us:
MCP belongs in the feature list, not in the positioning. Same arc as multi-probe
consensus (UpWatch, 2026-07-27) — two candidate differentiators commoditised in
five weeks.
**v4.2.0 (2026-08-10) — the check-type count moved, 4 → 6.**
- **TCP monitors:** periodically opens a connection to any `host:port` (database,
  SMTP, SSH, broker, game server), measuring reachability and connect latency,
  DOWN on timeout or on an optional latency threshold.
- **DNS monitors:** assert on resolved records (`A`, `AAAA`, `CNAME`, `MX`, `NS`,
  `TXT`, `SOA`, `SRV`, `CAA`, `PTR`) matched `EXACT` / `CONTAINS` / `REGEX`, with
  an expected response code (so *"this name must not resolve"* works), a custom
  nameserver over UDP or TCP, and opt-in **drift detection** that notifies on
  record changes without flipping the monitor DOWN.
- Rest of the release is polish: UI facelift, empty states, batched status-page
  uptime queries, ICMP latest-latency gauge fixed under partial packet loss,
  case-insensitive sorting, SMTP env-var docs.
The DNS-drift behaviour is worth noting on its own — *notify without alerting* is
a distinct alert class, not just a check type. **Still single-node:** nothing in
4.2.0 touches remote or multi-node probing, so our structural gap holds.

**Where SolidPing holds:** 38 check types vs 6; distributed multi-region workers
and private/deported locations (Kuvasz probes only from the single node it runs
on — no cross-region confirmation); built-in on-call schedules and multi-step
escalation (Kuvasz hands off to PagerDuty).
**Parity worth noticing:** config-as-code. Kuvasz puts YAML-declared monitors on
its homepage; SolidPing has YAML export/import/apply via API + CLI and says so
almost nowhere. Kuvasz is genuinely ahead only on OTLP export (we expose
Prometheus) and the Home Assistant integration.
*Watch:* multi-node / remote probing (would close our last structural gap), and
the MCP server leaving experimental. Surfaced 2026-07-28 via @selfhosted_bot
Mastodon repost (bot-announced → intel, not a lead). v4.2.0 surfaced 2026-08-13
via @KuvaszUptime's own Mastodon post on techhub.social.

### URLGuardian — https://urlguardian.app
Native **macOS desktop app** for uptime monitoring: "Real-time uptime monitoring,
detailed DNS analysis, and instant outage alerts right from your native Mac app."
Launched as a 1-point, 0-comment Show HN on 2026-08-12
([49277497](https://news.ycombinator.com/item?id=49277497), by `madospace`).
Landing page is a JS-rendered shell; no pricing, no docs, no check-type list
published at the time of writing — the meta description above is essentially the
whole disclosed spec.
**Not in our lane, and the reason is structural, not size.** The probe is the
user's laptop: it monitors only while the Mac is awake, online, and running the
app, from exactly one vantage point that moves with its owner. That is a
different product category from an always-on server-side monitor — closer to a
developer utility than to SolidPing, Uptime Kuma, or Kuvasz. It cannot page
someone at 03:00, which is the entire job.
`track: false`. Logged so the name resolves if it reappears, and as the third
data point in a pattern this page should keep counting: the *"uptime monitoring"*
keyword now attracts launches from adjacent categories (Mac utility here, AI
content farm the same day — see the OpsMate entry below), which
inflates the raw mention count without adding a single real rival.

### "OpsMate" — no verifiable product (2026-08-13)
Surfaced as a dev.to post, *"OpsMate vs BetterStack: Latency, Status Pages, and
Alerting for Global SaaS"*, which reads as a normal competitor launch: free tier
of 1 site + 1 server, **$9/mo for 5 servers**, HTTP/HTTPS + SSL expiry + keyword
+ Linux CPU/RAM/disk checks, 2–3 probe locations against Better Stack's 30+,
built-in status page, Slack/webhook/digest alerting, and the claim of
*"90% of the monitoring functionality for 30% of the price"* without
*"enterprise bloat"*.
**No such product could be found.** The canonical link points at a blog path on
`yunshao.aicreditsapi.com` (403 to fetchers); `opsmate.io` is an unclaimed Framer
404, `opsmate.app` and `getopsmate.com` do not resolve, `opsmate.dev` returns a
Cloudflare 523 (origin unreachable), and `opsmate.com` is **Opsmate, Inc.** —
SSLMate's certificate-transparency company, an unrelated business. Web search for
the product returns nothing.
**Recorded as an artifact, not a competitor.** No registry entry, no `track` flag.
Its value is as evidence for a measurement problem this page has to live with:
the *"X vs BetterStack"* comparison genre — the exact genre of our twelve pending
`/vs/` pages — is now being generated at volume by AI content mills, complete
with plausible pricing tables for products that do not appear to exist. Two
consequences. (1) **Never enter a competitor number sourced from a comparison
article into `comparison.md`** — verify against the vendor's own site or an
implementation, the same rule OneUptime#2937 forced on advertised intervals.
(2) It strengthens, again, the 2026-08-01/08-10 conclusion: our comparison pages
cannot win on volume, only on being first-hand, dated, and verifiable.

### OneUptime — https://oneuptime.com
Open-source observability platform (uptime + APM + status pages + incident mgmt +
on-call), both SaaS and self-hostable. Likely the closest **functional** rival:
broader scope than SolidPing (APM, logs, sessions), heavier deploy footprint than
the single binary. Actively producing "Uptime Kuma vs OneUptime" SEO content —
tracking their moves maps where "uptime kuma alternative" demand lives. Surfaced
2026-05-05 via OneUptime/blog#93.

**Advertised vs shipped, self-hosted (2026-08-01).** OneUptime's product page
advertises **1-second check intervals**; a self-hosted install (v11.7.3, Docker
Compose, `oneuptime/probe:release`) floors at **1 minute**
([#2937](https://github.com/OneUptime/oneuptime/issues/2937), open since
2026-07-30, 0 replies). The reporter's stated workarounds are to "continue using
Uptime Kuma for 20-second checks" or "maintain a custom patched OneUptime Probe".
Two things follow. (1) A factual data point for the interval table in
`comparison.md` — SolidPing's verified floor is 10s self-hosted, unpaywalled.
(2) A caution that generalises past OneUptime: **advertised intervals in this
market are frequently SaaS-only figures**, so verify against implementations
before entering any competitor's number into a comparison. Do not turn this into
attack copy (§9 — no trashing competitors); it is a self-hosted-parity gap in a
peer OSS project, and the honest use is our own verified number.

### OpenStatus — https://www.openstatus.dev
Open-source status page + uptime monitoring (OSS + paid SaaS), 8k+★, SOC2-ready
angle, trusted by Cal.com / Documenso. Paid hosted $30/mo (Starter, 20 monitors,
1-min, 28 regions) → $100/mo (Pro, 50 monitors, 30-sec, OTel exporter, private
locations) → **$500/mo (Scale, added 2026, 10 status pages)** → Enterprise (SOC2,
SAML/SSO). Same OSS+SaaS shape as SolidPing but status-page-first vs
monitoring-first. Their "X vs OpenStatus" comparison-page template (Atlassian,
Better Stack, Checkly, Incident.io, Instatus, Status.io) is worth studying.
*Update 2026-07-12:* added the $500 Scale tier and raised monitor allotments
("same pricing, more monitors"); Starter/Pro headline prices unchanged.

### failover.io — https://failover.io
SaaS uptime + cron + SSL with cascading multi-channel alert escalation; the
alert chain pauses only on explicit **acknowledge** across 10 channels (Email,
SMS, Slack, Telegram, Discord, Teams, ntfy, PagerDuty, Webhook, Voice). Free
($0, 5 monitors, 60s, 7d history) / Pro ($19/mo, 50 monitors, 30s, cron,
SMS+voice) / Team ($79/mo, unlimited, 15s, on-call, 20 seats). Active-active HA.
Their ack-required model is the top differentiator — confirm SolidPing's ack flow
before writing any /vs/ content. Surfaced 2026-05-19 via Mastodon self-promo;
pricing captured 2026-05-21. *Update 2026-07-12:* added **blockchain / RPC node
monitoring with stall detection** and authenticated-endpoint support; Free/$19/$79
prices unchanged.

### Status Harbor — https://statusharbor.io
SaaS uptime + private-network monitoring via outbound "Lighthouse" agents that run
inside the network and push results out — no inbound ports (same concept as
SolidPing's distributed workers). HTTP/TCP/UDP/SSL/DNS + cron; Email/Slack/
Telegram/Webhook. Free ($0, 5 monitors, 1 Lighthouse, 5-min, HTTP/TCP/UDP only) /
Starter (**$10/mo**, 1-min, +SSL/DNS) / Pro (**$24/mo**, 25 monitors, 3 Lighthouses,
6 members). SolidPing counters:
self-hosted/OSS, 38 check types vs ~6, 10 channels vs 4. Surfaced 2026-05-25 via
an r/selfhosted self-promo that was heavily downvoted ("AI slop advertisement") —
community mood is hostile to promo posts here. *Update 2026-07-12:* paid prices
**roughly doubled** (Starter $5→$10, Pro $12→$24); Lighthouse agents now also stream
host metrics (CPU/mem/disk/net).

### StatusDrift — https://statusdrift.com
Uptime + cert + domain + DNS + TCP, status pages, incident mgmt, SLA monitoring,
on-call. Self-described "tired of the state of monitoring and ITSM tools" — feature
breadth overlaps SolidPing's positioning very heavily; scope-wise one of the
closest SaaS competitors seen. Cloudflare anti-bot blocks auto-scrape of pricing.
Surfaced 2026-05-05 via an HN self-promo comment (item 47741527).

### Checkmk — https://checkmk.com
Enterprise IT observability (servers, networks, cloud, K8s, DBs, IoT); 2000+
integrations; Community (OSS) + Pro/Ultimate (self-hosted) + Cloud (SaaS).
Nagios/Zabbix generation — agent-based host monitoring for enterprise ops/MSPs,
with synthetic monitoring as one small feature. A homelabber choosing "self-hosted
uptime + on-call" wouldn't reach for it, but it co-appears in broad "monitoring"
searches. Don't chase its enterprise buyers; position SolidPing as
"simpler, lighter, purpose-built for uptime + on-call." Added 2026-05-18.

### HarborScale — https://harborscale.com
SaaS "Monitor Everything in 60 Seconds — No Setup Required." Tagline mirrors
SolidPing's ease-of-onboarding angle. Docs use IoT metaphors (Harbors = projects,
Ships = entities, Cargo = metrics) suggesting broader device/service monitoring.
Pricing/check types not yet captured (JS-rendered). Added 2026-05-18.
*Update 2026-07-12:* **pivoted toward a telemetry / observability platform** —
ingests metrics from Linux servers, Docker containers, and ESP32/IoT sensors with
managed Grafana built-in. Lower direct-uptime overlap now; downgrade priority.

### exit1.dev — https://exit1.dev
Ultra-cheap SaaS ($3/mo historically) with broad protocol coverage (HTTP/REST/UDP/TCP/ICMP/
Heartbeat/WS/DNS/SSL/domain expiry) and unlimited webhook alerts. Disruptive on
price; overlaps SolidPing's network/security checks. SolidPing wins on self-host,
multi-tenant, on-call, databases/MQ/browser/JS; exit1 wins on "pay $3 and forget
it." Surfaced 2026-04-12 (Show HN 47736853). *Update 2026-07-12:* now advertises an
**MCP server for AI assistants** (same category as SolidPing's MCP surface) plus
WebSocket + ICMP checks; free tier 10 monitors, paid scales to 1,000 monitors @15s
(exact cheap-tier price unverified on the current site).

### SolidUptime — https://soliduptime.org
SaaS uptime with incident grouping, free tier, no CC. **Name-collision risk** —
"SolidUptime" / "solidping" are confusably similar; both launched 2026, both
emphasize incident grouping. Matters for SEO / trademark / domain. Surfaced
2026-04-07 (Show HN 47675648). *Update 2026-07-12:* **no live product found — the
site/product did not surface in re-search; presumed inactive/parked.** The
name-collision concern is therefore lower priority until/unless it reappears.

### Updu — github.com/nwpeckham88/updu
Lightweight single-binary self-hosted uptime monitor (OSS). Single-binary like
SolidPing but uptime-only — no on-call, status pages, or protocol breadth. The
natural step-up target when minimalists outgrow it. Surfaced 2026-04-08 (HN 47689467).

### Pulsorup — https://pulsorup.com
Solo-dev SaaS positioned on "fewer false alerts" — competes with SolidPing's
incident grouping / adaptive resolution. SolidPing counters with self-host,
multi-protocol, on-call. Surfaced 2026-04-18 (HN 47819121). *Update 2026-07-12:*
still live; pricing unverified.

### Larm — https://larm.dev  (added 2026-08-10)

SaaS uptime monitoring, EU-hosted, one-person product (About page signed
"Johanna", ex-on-call engineer; stated motivation is alert blindness from false
positives). Surfaced via the author's own comment in *Ask HN: What are you
working on? (August 2026)* — [HN 49235194](https://news.ycombinator.com/item?id=49235194),
author `shintoist`.

**The closest architectural match in this catalogue.** Independently built, but
the same design as SolidPing: an Elixir/Phoenix control plane on a 3-node
cluster, with the actual probes as **small Go binaries spread across multiple
hosting providers worldwide**, plus synthetic checks running in per-check
Playwright sandboxes.

| | |
|---|---|
| Check types advertised | HTTP, TCP, DNS, Heartbeat (4) + synthetic/browser |
| Alerting model | Multi-region **majority vote** before alerting |
| Depth | **Per-check request traces**: DNS → TCP connect → TLS handshake → TTFB → content transfer, per phase, per location, trended |
| AI | MCP server |
| Hosting | EU; "EU-only probe control" gated to the top tier |
| Self-host | **No** — SaaS only |

Pricing verified 2026-08-10 (annual billing):

| Tier | Price | Monitors | Interval | Retention |
|---|---|---|---|---|
| Free | $0 (free for commercial use) | 15 | 3 min | 90 days |
| Pro | $19/mo ($228/yr) | 100 | 1 min | 1 year |
| Business | $49/mo ($588/yr) | 500 | **30 sec** | 2 years |

Heartbeat interval floors at 10 s on Business. Full API access on every tier
including Free.

**Where SolidPing wins, on verifiable facts:** protocol breadth (38 check types
vs 4 + synthetic); self-hosting (Larm has none); and check interval — Larm's
floor is 30 s *and* paywalled at $588/yr, against SolidPing's 10 s self-hosted,
unlimited and not tier-gated. See the Axis 5 confirmation in `positioning.md`:
Larm is the strongest evidence for that axis precisely *because* it built the
same distributed architecture and still did not go below 30 s.

**Where Larm is genuinely ahead — flagged for engineering, not for copy.**
Phase-level per-check request traces (DNS/TCP/TLS/TTFB/transfer, per location,
trended) are real depth SolidPing does not match. It is the substantive version
of "not just up or down", and it is the kind of capability that takes
implementation rather than a landing-page sentence — i.e. by our own rule, an
axis Larm could legitimately hold. Worth sizing.

Larm's homepage names Uptime Kuma directly — *"Unlike single-location monitors
like Uptime Kuma, Larm confirms from multiple regions before alerting"* — so it
is fishing in the same Kuma migration pool. `track: true`.

*Consensus tally: Larm is the 4th product in ~6 weeks to headline multi-probe
consensus, after Vigilmon (06-28), UptimeMonitoring.com (07-21) and UpWatch
(07-27). The claim is settled commodity copy.*

### New self-hosted entrants (added 2026-07-12)
Surfaced during the 2026-07-12 refresh via "Uptime Kuma alternative" roundups and
self-hosted discovery. Early — verify traction before treating as head-to-head.

- **Tianji** — https://tianji.msgbyte.com · OSS, free. Frequently ranked the top
  self-hosted Uptime-Kuma alternative; bundles uptime + website analytics + server
  status in one app. Broader "all-in-one" scope than SolidPing's uptime-first
  focus; watch its momentum.
- **EasyMonitor** — batteries-included self-hosted Uptime-Kuma alternative, one
  `docker compose up`. Stack: Laravel 12 + PostgreSQL + Redis Streams + Go probes +
  TimescaleDB for results. Same self-host lane; no distributed multi-region or
  on-call depth surfaced yet.
- **Kener** — https://kener.ing · OSS (SvelteKit), lightweight status-page + basic
  monitoring. Status-page-first, narrower than SolidPing; SEO competitor for
  "open-source status page" queries.

---

## Background / adjacent / low-threat (`track: false`)

Logged for awareness — server-stats tools, APM suites, cron-only niches, hackathon
projects, content-farm SEO, and paid templates. Not head-to-head rivals.

- **Beszel** (beszel.dev, OSS MIT) — lightweight server-stats (CPU/mem/net/Docker),
  not uptime/synthetic. A *complement* to SolidPing, not a substitute — say so in
  any thread that recommends it.
- **Komari** (github.com/komari-monitor/komari) — OSS server-stats (agent +
  dashboard), same class as Beszel/Netdata. No functional overlap. Surfaced 2026-05-08.
- **Moneat** (moneat.io, Kotlin, 82★) — self-hosted "drop-in for Sentry + Datadog,"
  full-stack observability incl. on-call/uptime topics. APM-class (SigNoz lane);
  a team picking it is replacing Datadog/Sentry, not choosing an uptime tool.
- **SigNoz** (signoz.io) — OSS observability suite (APM/logs/traces/metrics).
  Competes with Datadog/New Relic; only an SEO competitor for "uptime alternative"
  queries. Don't compete on APM.
- **PulseGrid** (pulsegrid.softadastra.com) — C++ real-time monitoring, appears
  system-metrics-focused. Minimal traction (Show HN 47861872). Needs overlap
  confirmation.
- **AnchorFlow** — API degradation detection (gradual slowdowns vs binary up/down).
  Complementary angle; SolidPing's JS/API checks could cover similar cases.
  HN-only (47848862), no homepage.
- **Crontinel** (crontinel.io) — Laravel-native cron-job monitoring, capitalizing
  on the thenping.me shutdown. Heartbeat-only, narrow. Adjacent to SolidPing's
  heartbeat check type; won't compete on multi-protocol/self-host.
- **RK Cron Monitor** (api.rkcron.com) — cron SaaS pitching "detect missed crons
  without heartbeats," which is really schedule-validated heartbeats (Healthchecks
  already does this via grace windows). Very early, API-only landing.
- **Tickstem** (github.com/tickstem) — SDK-first dev tools (cron/heartbeat/uptime/
  email verification) in Go/Python/Node + an MCP server. Devs *import a library*
  rather than run an external probe. All repos ★0-2. Track the "SDK-first
  monitoring" positioning more than the tool.
- **Velprove** (velprove.com) — SaaS uptime pushing "Uptime.com alternative" /
  "Oh Dear alternative" SEO content on dev.to. Small/early; recorder-style
  end-to-end UX is its hook.
- **Statsy** (statsy.io) — indie status page + auto uptime monitoring, "2 minutes
  to set up." Founder posted r/SaaS "how to get first users?" — very early.
- **Upplink** (flyver.app/upplink) — paid Next.js + Supabase status-page template,
  one-time $49. Not SaaS/OSS; community routes people to Uptime Kuma/Gatus. SEO noise.
- **Beacon** (github.com/Bajusz15/beacon, Go, Apache-2.0, 16★) — self-hosted CLI
  for secure remote access (no open ports, CGNAT-safe) with health checks as a
  bonus. Home-lab focus, not team uptime. Watch if it adds heartbeat/cron + multi-tenant.
- **PingWatch** (pingwatch.org) — hosted HTTP-only Uptime Kuma alternative,
  "no server, no Docker," 5-min checks, Free + $7/mo. Very narrow; evidence the
  "hosted Kuma alternative" SEO niche is crowded. Surfaced 2026-06-09 (HN 48458536).
- **LitePing** (github.com/LavanuruRohithRoy/LitePing) — student hackathon project,
  Python/FastAPI self-hosted uptime + cron, no UI, no traction. Surfaced 2026-06-05.
- **Vigilmon** (vigilmon.com) — SaaS + *claimed* MIT self-host pushing multi-region
  consensus alerting ("only alert when a majority of regions agree → zero false
  positives"). Content-farm origin: 7+ programmatic "X vs Vigilmon" dev.to posts
  2026-06-26 (all 0 pts), claimed OSS repo 404s, publishing org hosts ~10 zero-★
  AI-generated repos. Reads as vaporware/pre-launch shell. Its multi-region framing
  = SolidPing's distributed-worker story — a reminder to own that narrative in copy.
- **probes.dev** (probes.dev) — bootstrapped $5/mo HTTP-only SaaS by Blain Smith
  (real Go dev, genuine Fediverse presence). Verbatim pitch: "uptime monitoring
  tools have too many features, confusing pricing, and aren't built for small
  teams or indies." First paying user 2026-06-22. HTTP-only, SaaS-only. Contests
  the exact "monitoring is too complex/expensive for small teams" narrative
  SolidPing wants to own — real human, could gain traction.
- **Hesklo** (dev.to/expertblink) — indie "most flexible uptime monitoring tool,"
  escalation-flow hook. No homepage/pricing captured; SaaS-vs-OSS unknown. Zero
  engagement on launch. Escalation emphasis overlaps SolidPing's on-call.
- **UptimeMonitoring.com** (uptimemonitoring.com, by Monitive) — MCP-FIRST SaaS,
  "API-first uptime monitoring for deploy pipelines, developers, and AI agents."
  Ships an MCP server for Claude/ChatGPT/Cursor (natural-language monitor
  create/manage), 22 global probe locations with cross-region failure
  confirmation. HTTP/HTTPS only (DNS/TLS/connect/TTFB/download timing breakdown),
  SaaS-only (not self-hostable). Free ≤50 monitors (100 early), 60s intervals,
  30-day retention, no card; paid tiers planned (30s intervals, longer retention,
  response-time alerts). Alerting via webhooks / browser push / RSS / MCP queries
  — **no email**. Surfaced 2026-07-21 (Show HN 48919840, 3 pts / 0 comments,
  author `luciandan`). Significance: first rival built MCP-FIRST (not a bolt-on
  MCP endpoint like Tickstem/Uptime.com/exit1.dev) — confirms MCP is now contested
  table-stakes. SolidPing's edge holds on 38 check types (vs HTTP-only), OSS
  self-host (vs SaaS-only + hosted MCP), and built-in on-call. Watch: paid pricing
  and whether it adds non-HTTP check types.
- **Watchpost** (github.com/brod-dev/watchpost) — "Tiny self-hosted uptime monitor
  — one Go binary, live dashboard, no database." Notable only because it reuses
  SolidPing's own single-binary hook; there is no product behind it. 0★, 0 forks,
  no license, no homepage, created and last pushed 2026-07-11 (~14 minutes of
  commits). Non-threat; recorded so the name is not re-triaged. Surfaced 2026-07-26.
- **yoself** (codeberg.org/nykula/yoself) — drop-in `compose.yaml` service that
  reads the Podman socket and publishes a container status page, configured by a
  few env vars. Adjacent, not head-to-head: host-local container health only, no
  external probing, no protocol breadth, no alert routing. Overlaps on the
  "self-hosted status page" keyword alone. Surfaced 2026-07-26 via author
  self-promo on lemmy.world /c/selfhosted.
- **UpWatch** (upwatch.online) — indie SaaS, "built for indie hackers." HTTP GET
  probes only, with retries and SSRF protection. Free £0 (5 monitors / 15-min
  interval / email), Pro £10/mo (50 monitors / 5 min / Slack + Discord),
  Business £30/mo (unlimited / 1 min / Telegram + webhooks / **triple-probe
  consensus**). Public status page on every tier; the "dedicated" status page it
  advertises (status.upwatch.online) is a hosted **Uptime Kuma** instance.
  Surfaced 2026-07-27 (HN 49062533, 1 pt / 0 comments, author `snookiebaby`).
  Non-threat on capability — HTTP-only, SaaS-only, coarse free interval — but it
  is the **third product in five weeks** to headline multi-probe consensus
  (Vigilmon 2026-06-28, UptimeMonitoring.com 2026-07-21, UpWatch now). Read that
  as: "we confirm from several vantage points" is commodity copy in 2026, and the
  defensible half of SolidPing's distributed-worker story is that the probes run
  *inside the customer's own network*, self-hosted.
- **Temps** (temps.sh · github.com/gotempsh/temps, Rust, Apache-2.0, **563★**,
  created 2025-10, pushed daily) — self-hosted all-in-one PaaS: "Open-source
  alternative to Vercel + Sentry + PostHog + Pingdom." Git-push deploys,
  analytics, session replay, error tracking, managed Postgres/Redis/S3,
  transactional email — **and uptime monitoring**, listed in its README
  comparison table as replacing "Better Uptime / Pingdom ($20+/mo)" at $0.
  What it actually ships is *platform-internal* monitoring: deploy failures,
  runtime crashes, certificate expiry and backup health for apps deployed on
  Temps. No external synthetic probing, no vantage points off the host, no
  protocol breadth — the monitor shares fate with the machine it watches, so a
  host-level outage takes the alerting down with it. That shared-fate argument is
  the honest counter, and it generalises to every bundled built-in
  (Coolify/Dokploy/Zoraxy).
  `track: false` as a monitoring product, but this is the largest **bundler** in
  the registry (563★ vs Moneat's 82★), and bundling is the real threat model for
  the self-hoster segment: nobody installs a second tool for something their
  platform claims to already do. Flip to `track: true` if Temps ships genuine
  off-host probing or starts ranking for "uptime monitoring" queries.
  Surfaced 2026-07-27 via its own PR #446.
- **Tindra** (tindra.dev, @blendbyte) — EU/German self-hosted Sentry alternative
  bundling "errors, performance, uptime and cron monitoring in a single Go binary
  + Postgres", self-host or vendor-hosted in the EU. Pitch is anti-Sentry
  operational weight: "Sentry's self-hosted docker-compose.yml defines 71 services
  by default. We wanted one." Surfaced 2026-07-28 (mastodon.social self-promo).
  Traction unverified — no public repo found, licence unstated, homepage is
  JS-rendered and was not extracted. Second **bundler** in two days (after Temps)
  and the second entrant in three days to reuse the "one Go binary" hook (after
  Watchpost): monitoring keeps arriving as a feature of something else. The
  shared-fate counter that applies to Temps applies to its uptime half too if the
  monitor runs beside the app it watches. Re-check for a repo, licence and pricing;
  flip to `track: true` only on external probing plus real traction.

### Overcheck — github.com/overcheck/overcheck
Self-hosted uptime monitoring, TypeScript, AGPL-3.0, created 2026-07-07, last
push 2026-07-20, **0★**, no repo description. Show HN launch surfaced 2026-08-05
— i.e. the repo was already two weeks stale at its own launch. `track: false`;
no traction to speak of.
Notable only for its feature selection: the Show HN headline is "self-hosted
uptime monitoring, **API and multi-user access**", which is two of the three
things SolidPing leads with against Uptime Kuma. Kuma's single-user ceiling is
now common knowledge, and new entrants aim straight at it — that shared target
is worth more than this particular project.

### Sentivel — https://www.sentivel.com
SaaS status pages with uptime monitoring built in, plus **dependency tracking**:
it watches the upstream providers you build on and surfaces their incidents on
your own status page. Positioning line: "Bad days happen. Have a good page." /
"Customers will forgive an outage. They won't forgive silence." Free tier, no
card required. Show HN launch surfaced 2026-08-05. `track: false` — SaaS-only,
brand new, traction unverified.

**Why it is worth more attention than its star count.** Every notable entrant
since late June crowded an axis SolidPing already occupied — multi-region
consensus (Vigilmon, UptimeMonitoring.com, UpWatch), bundling (Temps, Tindra),
MCP (Kuvasz). Sentivel is the first in weeks to open a *different* question:
not "is my service up" but "is anything I depend on down". Nobody else in this
catalogue does upstream-dependency status aggregation, and for a team sitting on
a stack of managed services it is a real question.
If it gains traction, the strategic call for SolidPing is whether
upstream-dependency awareness belongs in the product at all or is honestly a
different product. Recording the angle now so the decision is not made under
time pressure later.

### WatchCat — https://watchcat.io  (added 2026-08-10)
SaaS uptime + cron/heartbeat monitoring, Rails, EU-hosted. Surfaced in the same
*Ask HN: What are you working on?* thread
([49234346](https://news.ycombinator.com/item?id=49234346), author `_spl`).
Uptime checks from multiple regions, cron/heartbeat monitoring, incidents,
status pages, notifications (Slack, Discord, Telegram, Google Chat, webhooks,
email), API, and "manage monitors from your AI agent". The author's own framing
is accurate: *"Nothing revolutionary — the goal is to make the familiar stuff
simple, reliable, and pleasant to use."*

Pricing verified 2026-08-10: Free €0 (5 monitors, 3 min, 7-day retention, 1
status page) · Pro €19/mo excl. VAT (50 monitors, 1 min, 90-day, 3 status
pages) · Team €49/mo excl. VAT (200 monitors, 1 min, 365-day, 10 status pages).
**Check frequency floors at 1 minute on every tier, including the top one** — a
6× gap against SolidPing's unpaywalled self-hosted 10 s.

`track: false` — SaaS-only, no self-host, nothing SolidPing loses a deal to in
its lane. Catalogued for the **positioning pattern**, not the product: WatchCat
is the purest example of EU data residency being sold as the headline rather
than a feature ("a clear data path, not vague residency promises", plus a
published compliance brief). That pattern — three products in two weeks, with
Tindra (07-28) and Larm — is analysed in `positioning.md` under *Axis 6
candidate*, where it is **rejected as an axis for SolidPing** (any EU vendor
writes the same sentence next week) and kept as demand signal for jurisdiction
control, which self-hosting answers as a superset.
