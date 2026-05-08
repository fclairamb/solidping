# Market Feedback: What Users Hate, Want, and Will Pay For

Synthesis of public user sentiment about uptime/incident-management tools — BetterStack and the competitive set — pulled from Hacker News, Indie Hackers, dev.to, LowEndTalk (search-cache only), vendor comparison posts citing G2/Capterra reviews, and SaaS review aggregators.

This complements the feature-focused write-ups in [`docs/competitors/`](../competitors/) by capturing what people *say about* these tools rather than what the tools *do*.

> **Source caveat.** Reddit was unreachable from this research pass (`reddit.com` returned 403 on every fetch). Where Reddit is referenced below it's via second-hand summaries surfaced by web search. Direct quotes are from HN (Algolia API), Indie Hackers, dev.to posts, and vendor-curated review aggregations. Primary URLs are listed in the [Sources](#sources) appendix.

---

## TL;DR

Five recurring complaints across the category:

1. **Pricing is a moving target.** Per-monitor, per-responder, per-subscriber, per-test-run, and per-region surcharges stack until the bill surprises the buyer. Flat pricing is the most-asked-for feature.
2. **The free tier is a trap.** Vendors retroactively re-tier or restrict commercial use; users who built habits on the free plan get burned.
3. **False positives erode trust faster than missed alerts.** Single-location monitors and over-eager alarms train users to ignore pages — the worst possible outcome.
4. **The bundle is fragmenting again.** BetterStack's pitch ("replace PagerDuty + Pingdom + Statuspage") is genuinely loved, but the same buyers complain it's too much to learn and too expensive once you actually use the bundle.
5. **Status pages are oversold and under-built.** $299/mo for Atlassian Statuspage with little perceived investment is the canonical complaint; everyone knows there's a market gap here.

The strongest "I would pay for this" signals are: **flat predictable pricing**, **multi-region by default with confirmation logic**, **status page that doesn't require buying monitoring twice**, **a mobile app you can actually act in**, and **API/IaC-first configuration**.

---

## 1. Pain points by competitor

### BetterStack

The most-praised tool in the dataset *and* the most-complained-about — both because it ships the broadest bundle and because the bundle prices nonlinearly.

| Theme | Specifics |
|---|---|
| **Unpredictable bill** | Per-50-monitors ($21/mo) + per-responder ($29/mo) + per-host log volume + Playwright-minute usage. Predicting next month's invoice "requires careful estimation" (hyperping.com guide citing G2). |
| **Support gating** | Phone support reportedly only for accounts above $2,000/mo spend. |
| **No self-hosting** | SaaS-only is a hard no for buyers with data-residency or air-gap requirements. |
| **Multi-team UX** | Users can't view a unified dashboard across teams without switching contexts. |
| **Mobile app is read-only** | Notifications work; incident management does not. |
| **Synthetic monitoring less mature** | Weaker than Uptime.com / Checkly for complex browser flows. |
| **Learning curve** | Reviewers note "dedicated onboarding to unlock the platform's full potential" — fine for buyers, painful for indie devs. |
| **Limited integrations** | Compared to dedicated incident tools (PagerDuty, Opsgenie). |
| **Itself goes down** | StatusGator records 72 outages over 2 years (≈2.6/mo, 73-min mean recovery). One past incident: status pages were serving the wrong customer's data (HN item 38023092). |

**Voice of the user:**

- listennotes blog post (HN id 42966015): pitches BetterStack as "replace PagerDuty, Datadog, and Statuspage — cut costs by 90%" — that *is* the appeal.
- IndieHackers user `codeWhisperer`: *"Used many over the last 15 years — I recommend BetterUptime / now BetterStack."*
- Uptime.com / Hyperping comparison guide: *"Costs can climb quickly once you need more than the basics."*

### Pingdom (SolarWinds)

Acquired by SolarWinds for $103M in 2014. Universal pattern in feedback: *quality plateaued, pricing didn't.*

- *"After SolarWinds acquired Pingdom, development slowed and pricing changed."* (oneuptime guide)
- Per-check pricing: 50 URLs × 1-min interval runs into hundreds of dollars/mo.
- HN item 2680054 (`Tell HN: false alarms from Pingdom`) and HN item 20265611 (`Pingdom was down 3 hours today`) — recurring trust hits.
- Trustradius/G2 surface plain complaints: *"It is not cheap for all the options."*

### UptimeRobot

The default "indie/free" choice — and increasingly the default cautionary tale.

- **November 2024 retroactive ToS change:** free plan restricted to non-commercial use, with notice landing during US Thanksgiving (HN id 42244667 — `Tell HN: Uptimerobot.com offers a fake free plan`).
- HN user `evanelias`: *"never ever trust this service for anything"* after 26-day notice during a holiday weekend.
- HN user `fer`: *"I am a free user for non-commercial purposes and they're terminating me if I don't upgrade."*
- **2025 price hike** documented by an indie dev on dev.to (`driftdev`): bill went **$8 → $34/mo (4×)** without service changes. He shipped Uptimely as a $9/mo flat-priced response.
- **UI complaint, much louder than people expect:** Indie Hackers / G2: *"UI is unbearable. After multiple years I can't put up with it."*
- 5-min interval on free is too slow for users wanting "fast detection."

### Atlassian Statuspage

The single most-griped-about product in the category.

- $299/mo for the 5,000-subscriber tier; $999/mo at the top.
- Pricing is **per-subscriber tier**, which scales painfully as your user base grows — the company *succeeds* and gets billed for it.
- Universal critique: *"Statuspage is a communication tool, not a monitoring tool"* — buyers must pay separately for the thing that detects incidents, then plumb it into Statuspage.
- *"Many users feel the product has been under-invested relative to its pricing."*
- Show HN item 47285913 explicitly framed as: *"$15/mo Status Page (vs Atlassian $299)"* — that's a positioning that lands.

### PagerDuty

- $21/user/month basic; $105/mo for a 5-person rotation **just for the alerting layer**, before you've paid for monitoring.
- Basic tier "misses many essential functionalities" — gating drives upgrades.
- **Opsgenie shutdown** (Atlassian ended new sales June 2025, full shutdown April 2027) is forcing the previously cheaper alternative's customers into the market right now.
- HN item 25650905 (120 comments) on on-call: alert fatigue and uncompensated rotations are real, but they're a *people* problem; the tool can either inflame or soothe it.

### Datadog

- *"Bills surprise teams"* is the headline.
- Synthetic API testing $5/10k runs, browser $12/1k runs, mobile $50/100 runs — sounds cheap; full multi-region high-frequency coverage adds up to thousands.
- Per-product pricing means full observability stack (infra + APM + logs + synthetics + RUM + incidents + error tracking) is "individually reasonable, total fast."
- Considered overkill for small teams and indie devs.

### Checkly

- Hobby tier is generous (10k API + 1.5k browser checks) but **rigid pricing tiers** are the top complaint: pay for the slot, not your usage.
- Steep learning curve (config-as-code, Playwright knowledge required).
- Cost grows fast at scale; browser checks are expensive due to Chromium overhead.

### Healthchecks.io

The unicorn — almost universally praised. The complaints are revealing:

- **No built-in status page.** Users repeatedly ask for it.
- **Pricing-tier gap:** free → 20€/mo Business jumps too far; users want a $5–10 middle.
- **Bus-factor risk:** *"a one-person project, which eventually might suffer from being such."* Buyers want redundant maintainership for prod use.
- **UI clunkiness** — *"simple but not always intuitive."*
- Praise: *"Easy to set up and once set, you can forget about it knowing you will be alerted to any issues."*

### Uptime Kuma (self-hosted)

The default "I'll just self-host" answer in r/selfhosted threads. Limits that push people back to SaaS:

- **Single-location checks** → false-positive prone (the #1 reason cited for outgrowing Kuma).
- **No SSO** beyond basic admin auth + 2FA — blocks enterprise.
- **No on-call / escalation** — Kuma notifies, it doesn't *manage*.
- **No REST API** — a real obstacle for IaC shops.
- **The catch-22 of self-hosted monitoring:** if your monitor is on the same infra it monitors, an outage takes both down. Users discover this the hard way.
- v2 migration broke many users (SQLite → MariaDB has no supported path).

### Gatus / OneUptime / Healthchecks.io self-hosted

Loved for code-as-config, hated for "now I'm running another stateful service." Gravitate toward teams that already operate Postgres + Kubernetes.

---

## 2. Cross-cutting themes

### 2.1 Pricing structure complaints

The category has experimented with every billing axis at once: per-monitor, per-responder, per-region, per-test-run, per-host-log-volume, per-subscriber, per-user, per-team. Stacked together, the bill becomes opaque.

**What users keep asking for:**

- **Flat-rate plans.** OneUptime, Uptimely, Instatus all lead their marketing with "flat pricing" — that's a tell.
- **Predictability over absolute price.** "$50/mo I can budget" beats "$30 average that spikes to $120 once a quarter."
- **No surprise overage.** Soft-cap rather than auto-charge; warn before paying.
- **No commercial-use cliffs** on the free tier.

**Anti-pattern:** UptimeRobot's "fake free plan" framing has stuck. Once users believe a free tier is a bait-and-switch, they don't come back.

### 2.2 False positives — the trust killer

> *"About one in five alerts was a false positive caused by network routing issues, transient DNS problems, or a brief hiccup at the monitoring provider's own data center."* — dev.to khrome83

Single-location monitoring is widely understood to be broken — but most cheap tiers still default to it. Confirmation logic (require N-of-M regions to fail before paging) is the standard fix and *cuts false positives by >90%* (cited by Hyperping, Odown).

This is a hidden quality lever: a cheap tool with bad confirmation logic costs the user *sleep and trust* every week.

### 2.3 Alert fatigue & the on-call human cost

HN's on-call thread (item 25650905, 120 comments) is a goldmine for understanding the *emotional* cost of bad alerting:

- `ublaze`: catch-all alarms designed "to page when something looks suspicious enough to merit human attention, even if not a known failure case" create noise without action.
- `patrakov`: the SRE rule is *every page must demand a specific action* — "crying wolf" is an antipattern.
- `jrockway`: paged nightly for 15–30 min causes "stress spikes and reflects underlying reliability issues."
- `encoderer`: without authority to fix root causes, on-call becomes unsustainable firefighting.

**Tools that page noisily lose customers slowly.** Auto-grouping, deduping, severity-aware routing, and "snooze with reason" features matter more than people admit in surveys.

### 2.4 Trust & vendor longevity

Three patterns destroy trust permanently:

1. **Acquisition stagnation.** Pingdom → SolarWinds, Opsgenie → Atlassian shutdown. Buyers learn to discount whatever the founder promises about future investment.
2. **Retroactive ToS changes.** UptimeRobot's free-plan cliff is a textbook example.
3. **The monitor goes down with you.** When BetterStack's status pages served the wrong customer's data (HN 38023092), it confirmed a deep fear.

**Implication:** the marketing copy that lands isn't "more features" — it's "stable ownership, predictable pricing, no retroactive changes."

### 2.5 The fragmentation–bundle pendulum

The Runmon Show HN (HN 47136038) captures the mood:

> *"Cron silently fails → user reports it → I check logs → job hasn't run in days. These solutions are spread across 4 tools that each cost as much as a full SaaS subscription"* — Cronitor + Hookdeck + MXToolbox + BetterStack.

Indie devs want **uptime + heartbeat + cron + email + DNS + SSL + status page** in one purchase. BetterStack delivers this — but at the price of bundle complexity. There's a real opening for a *simpler* bundle priced honestly.

### 2.6 Mobile is undershipped across the board

Common across every review: mobile apps deliver notifications but don't let you *act*. Acknowledge, escalate, snooze, and run-a-quick-recheck-from-this-region from the phone are universally requested and rarely shipped.

### 2.7 Status pages are a de facto separate market

Atlassian's $299 entry point created a sub-category of "$5–$25/mo status page" startups (Instatus, Statuspal, Hund, etc.). Users repeatedly complain that:

- Pricing per subscriber penalizes growth.
- Subscriber management is clunky.
- Customization (custom domain, branding) is gated to mid tiers.
- You're paying twice — once for monitoring, once to *display* monitoring.

The "all-in-one with a real status page" pitch is a winning frame.

### 2.8 The self-hosted ↔ SaaS migration loop

Self-hosting starts as a cost / control decision, runs into:

1. False positives from single-location checks.
2. The monitor's-own-uptime catch-22.
3. Missing SSO / RBAC for team growth.
4. No managed on-call rotations.
5. The maintainer-burnout cliff (Healthchecks.io concern).

Users who hit any of these often pay 5×–10× to migrate back to SaaS *and resent paying it.* A product that supports both modes (or makes the SaaS feel like the natural graduation from self-hosted) captures both sides.

---

## 3. What people would pay for

Synthesizing across the threads — features users explicitly say they'd pay for, ranked by signal frequency:

| # | Feature | Signal | Notes |
|---|---|---|---|
| 1 | **Flat, predictable pricing** | Very strong | Mentioned in every alternative pitch (Uptimely, OneUptime, Instatus, Hyperping). |
| 2 | **Multi-region with confirmation logic by default** | Very strong | Cuts false positives >90%; users blame single-location for every bad alert. |
| 3 | **Status page bundled with monitoring, no per-subscriber penalty** | Strong | Direct response to Atlassian Statuspage rage. |
| 4 | **Sub-minute check intervals on cheap tiers** | Strong | UptimeRobot's 5-min free is universally noted as too slow. |
| 5 | **Cron / heartbeat + uptime + status page in one product** | Strong | Runmon's framing. Indie devs hate paying 3–4 tools to cover the same surface. |
| 6 | **Real mobile app (act, not just receive)** | Strong | Cited in BetterStack and PagerDuty reviews. |
| 7 | **API / Terraform / config-as-code first** | Medium-strong | Engineering teams won't click through a UI for 200 monitors. |
| 8 | **Multi-step / business-logic API monitoring** | Medium | Chained API requests with auth + capture, à la Step CI / Datadog Synthetics. |
| 9 | **Modern protocol coverage** | Medium | gRPC, MQTT, Kafka, queue depth, websocket — emerging stack underserved. |
| 10 | **On-call scheduling without per-responder uplift** | Medium | PagerDuty's $21/user prices small teams out. |
| 11 | **Self-hosted option for compliance / air-gap** | Medium | Specifically blocks regulated industries from BetterStack. |
| 12 | **Voice / SMS / push as commodity, not luxury tier** | Medium | "All-you-can-alert" is a feature people brag about post-purchase. |
| 13 | **Honest free tier with a no-retroactive-change pledge** | Medium | Pure trust play, but post-UptimeRobot it's a marketable signal. |
| 14 | **Status of the monitor itself (transparent SLA)** | Low-but-rising | Distrust seeded by BetterStack's own outages. |
| 15 | **AI/LLM features** (incident summary, alert dedupe) | Low | Vendors push hard; users mostly indifferent unless it actually reduces noise. |

### Price ceilings observed

- **Indie / solo dev:** "Tools under $20/month are preferred — ideally with a free trial." Hard ceiling around $20.
- **Indie startup with revenue:** $50–$100/mo total monitoring spend tolerable.
- **Small team (5–20 eng):** $200–$500/mo, but only if it replaces 2–3 separate tools.
- **Mid-market:** above this, Datadog / New Relic territory and SolidPing isn't competing on the same axis.

### Price floors that fail

- $5/mo: too cheap to fund multi-region infra honestly. Buyers expect it to be a hobby project that disappears.
- "Free with credit card required": measured churn signal — users feel tricked.

---

## 4. Implications for SolidPing

These are pulled directly from the patterns above. They're meant as inputs to product strategy, not commitments — judgement calls remain with the team.

### 4.1 Pricing strategy

- **Lead with flat per-org pricing.** No per-monitor, no per-responder, no per-subscriber. A single line item. Use soft caps (e.g., "fair use up to N checks/min") rather than usage-based billing surprise.
- **Price honesty as a feature.** Public "we will never retroactively change your tier" pledge in marketing — directly counterpunches UptimeRobot.
- **Bundle uptime + heartbeat + cron + status page + on-call.** This is the BetterStack pitch but priced flat instead of stacked.
- **Indie tier under $20** with sub-minute checks and at least 2-region default. The $5–$15 sweet spot is owned by no one credible right now.

### 4.2 Product priorities the data supports

1. **Multi-region by default, with N-of-M confirmation as a one-click toggle.** Frame as "false-positive filter" in UI.
2. **Status page that lives with the checks** — no separate product, no per-subscriber pricing, custom domain on cheap tier.
3. **Heartbeat / cron monitoring as a first-class peer to HTTP**, not an afterthought (this is the Runmon-style fragmentation gap).
4. **Mobile app must support acknowledge / escalate / snooze / run-a-region-recheck.** Read-only is below the bar.
5. **API + Terraform provider + import/export from day one.** Already in `/api/v1/orgs/$org/checks/export|import` — keep investing.
6. **Protocol breadth that includes gRPC / MQTT / Kafka / queue-depth.** Differentiator vs Pingdom/UptimeRobot stagnation.
7. **Self-host option** with the SaaS as the "graduation" — even a community edition would shift trust signals materially.
8. **Confirmation logic + smart retries baked into the default check policy.** Don't make users opt into not-being-paged-falsely.

### 4.3 Trust positioning

- Publish own SLA + uptime data prominently. Do not host the SolidPing status page on SolidPing infra (or make it geographically + provider-redundant if you do).
- Versioned pricing: when pricing changes, existing customers stay on their original tier indefinitely unless they opt in.
- Founders / maintainers visible — answers the Healthchecks.io bus-factor concern.

### 4.4 What to *not* build

- ❌ Per-responder pricing for on-call. The PagerDuty $21/user mistake is well-trodden.
- ❌ Per-subscriber status page pricing. Atlassian is the cautionary tale.
- ❌ A free tier you'll later restrict commercial use on. Better to charge $5 from day one than retroactively re-tier.
- ❌ "Synthetic Browser Testing" as the next big bet — Checkly and Datadog have it, and the market is comparing on price-per-run, which is a race to the bottom.
- ❌ Heavy AI/LLM marketing copy. Users discount it.

### 4.5 Marketing angles that the data says will land

- *"Predictable bills. No surprise invoices."* — direct counter to BetterStack/Datadog complaints.
- *"One product. Uptime, heartbeats, cron, status page, on-call."* — Runmon framing.
- *"Monitor from N regions. We only page you when more than one agrees."* — false-positive narrative.
- *"Status pages without the Statuspage tax."* — Atlassian counterpunch.
- *"Self-hosted? Cloud? Both."* — captures the migration loop.

---

## 5. Open questions worth more research

- **Reddit content** — the strongest direct-user-voice channel was unreachable in this pass. Worth a follow-up with a different fetch path (Pushshift/archive, browser session) to pull threads from r/sysadmin, r/devops, r/selfhosted, r/webdev.
- **Quantitative pricing-pain magnitude** — anecdotes are strong but a survey (HN poll, Twitter, Indie Hackers) on "your last monthly monitoring bill" would calibrate the tier ceilings.
- **Voice vs SMS vs push preference** — strongly opinionated, weakly sourced. Worth a direct user interview.
- **On-call scheduling appetite outside engineering** — the PagerDuty alternative space is crowded; whether SolidPing's existing customers want this bundled or whether they're already on incident.io / Rootly is unknown.
- **Self-host adoption funnel** — would Uptime Kuma users actually pay $20–$50/mo to get SSO + multi-region + on-call without leaving the self-host model? Validates 4.2(7).

---

## Sources

### Hacker News threads (via Algolia API)

- [Show HN: Peeng — like Pingdom, but the other way around](https://news.ycombinator.com/item?id=36758292) — 72 comments, broad sentiment on heartbeat / Pingdom alternatives
- [Tell HN: Uptimerobot.com offers a fake free plan](https://news.ycombinator.com/item?id=42244667) — November 2024 ToS controversy
- [Show HN: Step CI – open-source alternative to Pingdom and Checkly](https://news.ycombinator.com/item?id=33468052)
- [Ask HN: Any luck negotiating better terms for on-call?](https://news.ycombinator.com/item?id=25650905) — 120 comments on on-call human cost
- [Show HN: Runmon — every essential monitor in one place](https://news.ycombinator.com/item?id=47136038) — fragmentation framing
- [Show HN: SiteReady — uptime monitoring and status pages for indie makers](https://news.ycombinator.com/item?id=47055275)
- [Use BetterStack to Replace PagerDuty, Datadog, and Statuspage – Cut Costs by 90%](https://news.ycombinator.com/item?id=42966015)
- [Betterstack status pages serving random clients status page](https://news.ycombinator.com/item?id=38023092)
- [Tell HN: false alarms from Pingdom](https://news.ycombinator.com/item?id=2680054)
- [Pingdom was down 3 hours today](https://news.ycombinator.com/item?id=20265611)
- [Show HN: Pre-Launch – $15/Mo Status Page (Vs Atlassian $299)](https://news.ycombinator.com/item?id=47285913)
- [Ask HN: Would you use a self-hosted uptime monitor with a public status page?](https://news.ycombinator.com/item?id=44391771)

### Indie Hackers threads

- [What website monitoring tool do you use?](https://www.indiehackers.com/post/what-website-monitoring-tool-do-you-use-c0fb1e595c)
- [Best UptimeRobot Alternatives](https://www.indiehackers.com/post/best-uptimerobot-alternatives-d9f0b390ed)
- [What do you use for monitoring your website?](https://www.indiehackers.com/post/what-do-you-use-for-monitoring-your-website-1d46b99fdf)

### dev.to / Medium

- [UptimeRobot raised their price 4× — I built an alternative](https://dev.to/driftdev/uptimerobot-raised-their-price-4-i-built-an-alternative-p57)
- [How we built cross-region uptime verification (and why single-location monitoring is broken)](https://dev.to/khrome83/how-we-built-cross-region-uptime-verification-and-why-single-location-monitoring-is-broken-24mo)
- [Your Uptime Monitoring Is Broken By Design](https://codematters.medium.com/your-uptime-monitoring-is-broken-by-design-1312c511093f)
- [10 UptimeRobot Alternatives in 2025](https://dev.to/cbartlett/10-uptimerobot-alternatives-in-2025-9db)
- [8 Statuspage Alternatives: Open-Source and Hosted Solutions for 2025](https://dev.to/maxshash/8-statuspage-alternatives-open-source-and-hosted-solutions-for-2025-1lij)

### Vendor comparison posts (cite G2 / Capterra reviews)

- [BetterStack vs Uptime.com vs Hyperping (100+ G2 reviews analyzed)](https://hyperping.com/blog/betterstack-vs-uptime-vs-hyperping)
- [Best BetterStack Alternatives — OneUptime](https://oneuptime.com/blog/post/2026-03-25-best-better-stack-alternatives/view)
- [BetterStack Review — Efficient.app](https://efficient.app/apps/better-stack)
- [Top 7 Better Stack Alternatives — Instatus](https://instatus.com/blog/better-stack-alternatives)
- [10 Best Pingdom Alternatives in 2026 — OneUptime](https://oneuptime.com/blog/post/2026-03-11-10-best-pingdom-alternatives-2026/view)
- [Uptime Kuma Alternatives — Instatus](https://instatus.com/blog/uptime-kuma-alternatives)
- [Best Uptime Kuma Alternatives for Growing Teams — Hyperping](https://hyperping.com/blog/best-uptime-kuma-alternatives)
- [How to Self-Host Uptime Kuma (And When You Should Use Something Better)](https://dev.to/therealfloatdev/how-to-self-host-uptime-kuma-and-when-you-should-use-something-better-6d7)
- [How to Reduce False Positive Alerts in Uptime Monitoring — Hyperping](https://hyperping.com/blog/reduce-false-positive-monitoring-alerts)
- [False Positives vs Real Outages — Odown](https://odown.com/blog/false-positives-in-uptime/)

### Status page space

- [10 Best Atlassian Statuspage Alternatives — OneUptime](https://oneuptime.com/blog/post/2026-03-10-best-statuspage-alternatives/view)
- [Statuspage Review & Alternative — Instatus](https://instatus.com/statuspage-alternative)
- [7 Best Statuspage Alternatives — BetterStack Community](https://betterstack.com/community/comparisons/statuspage-alternatives/)
- [Top 5 Statuspage Alternatives — Hyperping](https://hyperping.com/blog/best-statuspage-alternatives)

### On-call / PagerDuty alternatives

- [PagerDuty Alternatives 2026 — Runframe](https://runframe.io/blog/best-pagerduty-alternatives)
- [10 Best PagerDuty Alternatives — OneUptime](https://oneuptime.com/blog/post/2026-02-06-best-pagerduty-alternatives/view)
- [5 Best Pagerduty Alternatives — BetterStack Community](https://betterstack.com/community/comparisons/pagerduty-alternatives/)

### Datadog / Checkly cost analyses

- [The real costs of Synthetics monitoring: Datadog — Checkly](https://www.checklyhq.com/blog/how-to-spend-ten-grand-12-bucks-at-a-time/)
- [How Datadog's Pricing Actually Works — OneUptime](https://oneuptime.com/blog/post/2026-03-13-how-datadog-pricing-actually-works/view)
- [Datadog Pricing Main Caveats Explained — SigNoz](https://signoz.io/blog/datadog-pricing/)

### LowEndTalk (search-cache only — direct fetch returned 403)

- [UptimeRobot free plan will no longer be available for commercial use](https://lowendtalk.com/discussion/199126/uptimerobot-free-plan-will-no-longer-be-available-for-commercial-use)
