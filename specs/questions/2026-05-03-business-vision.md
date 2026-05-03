# Business Vision

This document records the business posture of SolidPing — what we charge for, what we don't, and why. It exists to constrain product, license, and architecture decisions: if a future change is in tension with one of these principles, the change requires re-opening this document, not a quiet one-off feature gate.

## The four principles

### 1. The product is fully featured for everyone — with two narrow, named exceptions
We do not gate features behind a paid tier *for the purpose of pushing people to pay*. Self-hosters and free SaaS users get the same checks, the same alerting, the same status pages, the same retention, the same integrations, the same dashboards.

The only exceptions, named and bounded:

- **SSO (SAML / OIDC / SCIM) is SaaS-only and paid-only.** Self-hosters get local accounts and OAuth-via-provider login; identity-federation features are reserved for paying SaaS customers. Reason: SSO is the universal enterprise checkbox; it is the cleanest, most-recognised conversion lever, and decoupling it from self-hosting keeps the rest of the product simple and uncompromised.
- **Quotas are SaaS-only.** Rate limits, retention windows, member counts, status-page counts are tier-driven on the SaaS. Self-hosted SolidPing has no quotas: users dimension their own infrastructure.

Everything else lives in the open codebase, available to everyone, with no flags or feature gates whose purpose is to push payment.

### 2. Self-hosting is free, unrestricted, and strategic
Anyone may run SolidPing on their own infrastructure, at any scale, indefinitely, with no usage caps, no commercial-license trigger, and no "heavy use must pay" clause. Self-hosting is not a degraded entry point we tolerate — it is a deliberate distribution channel.

The strategy: an engineer self-hosts SolidPing for their team, finds it useful, recommends it to peers, the tool spreads inside the company, and over time the company adopts the SaaS for the operational reasons in Principle 3 (and, for some companies, for SSO from Principle 1). Free self-hosting is the on-ramp; SaaS is the destination for organisations that grow into it.

This explicitly drops the earlier idea of a "Self-Hosted Commercial License" for heavy users. We don't want that conversation; we want the user to keep using the tool.

### 3. SaaS hosting is paid, and recommended
We charge for SolidPing.io. We also actively recommend it over self-hosting, on architectural grounds: **monitoring should not run on the same infrastructure it monitors**.

The pitch (Principle 5 below sharpens this further):

- **If your app and your monitor share an outage, your monitor failed at its only job.** That single sentence is the most honest version of why monitoring SaaS exists.
- **Multi-region probing**, hard to recreate with self-hosted workers in one DC.
- **Status pages that stay up when your stack goes down.** A status page on the same infrastructure as the thing it reports is the most-mocked outage trope in our industry.
- **Zero ops.** No Postgres to manage, no migrations, no version upgrades, no on-call rotation for the thing that's supposed to wake you up.
- **Meta-monitoring** (TLS expiry, certificate transparency, DNS reachability) — annoying to do for yourself, trivial for us.

Pricing tiers are defined in `2026-01-03-saas-pricing.md` and calibrated to make SaaS the easy default for most customers.

### 4. Nobody else may charge to host SolidPing as a SaaS
The license forbids third-party hosted "SolidPing-as-a-Service" offerings. AWS-style arbitrage — "we host your open-source product cheaper than you" — is the failure mode that pushed Mongo, Elastic, Redis, HashiCorp, and Sentry off OSI-approved licenses. We adopt the same posture from day one.

This means SolidPing is **not OSI Open Source**. It is *fair-code* / source-available with a non-compete clause. We accept the community-perception cost of that label.

## License recommendation

Given the revised principles — no usage-based licensing, free self-hosting forever, anti-cloud-arbitrage clause, narrow `saas/` carve-out for SSO and quotas — the cleanest license shape is:

**Primary recommendation: FSL-1.1-Apache-2.0 for the core, proprietary license for the `server/saas/` package.**

Why FSL:
- Single license for the core, predictable, well-understood.
- The 2-year non-compete clause directly enforces Principle 4 in license language. After 2 years, any given commit auto-converts to Apache 2.0.
- Sentry uses it, which is meaningful social proof in a still-young licensing space.
- Aligns naturally with the "fair-code" framing.

Trade-offs:
- Not OSI-approved during the 2-year non-compete window. Excluded from Debian main, Fedora, and parts of the OSS-purist community. This is the same cost Sentry, n8n, Mongo, Elastic, HashiCorp, and Redis paid; community pushback is loudest for ~6 months and then fades.
- During the non-compete window, you cannot offer "a competing product" — wording matters; FSL gives a clearer definition of "competing" than BSL's per-vendor variability.

Alternatives considered:

- **AGPLv3 for the core** (OSI-approved). Insufficient on its own for Principle 4: AGPL only obliges resellers to share modifications, not their hosting infrastructure. AWS could legally run unmodified SolidPing-as-a-Service. Would need to be combined with Commons Clause or a custom non-compete addendum, which gives up the OSI benefit anyway.
- **n8n-style Sustainable Use License**. Equivalent in spirit to FSL but less standardised. Would work; FSL is just the cleaner of two near-equivalents.
- **SSPL** (Mongo). Strongest anti-cloud-arbitrage stance but more controversial than FSL and broader than what we need.
- **BSL** (HashiCorp pre-fork, MariaDB). Older, longer non-compete (default 4 years), per-vendor variability via "Additional Use Grant." FSL is the deliberate simplification of BSL.

The `server/saas/` package (containing SSO, quotas, billing) carries a separate proprietary license and is excluded from the public OSS distribution. It runs only on solidping.io.

## What this implies for self-hosting

- Self-hosters get **the same binary** as the SaaS, full featured, except SSO is not present and no quota-enforcement code runs.
- No license keys. No paid features for self-host. No commercial-use clauses to worry about.
- Optional, opt-out telemetry for product analytics — anonymous version, OS, rough volume bucket. Documented clearly with a single env var to disable. The point is to learn what users do, not to gate.
- Upgrade path to SSO is "use the SaaS." For organisations with strict on-premise constraints that block SaaS use, SSO is genuinely unavailable. We accept that this excludes some enterprise segments (defence, certain healthcare, high-regulation finance). They are not our target.

## What this implies for SaaS

The pitch is **not** "more features than self-hosted" — that's almost untrue (only SSO and operational quotas differ). The pitch is *operational and architectural*:

1. **Independent failure domain** is the headline. Lead with it. The status page that stays up when your stack goes down is a concrete, memorable proof of the abstract principle.
2. **Multi-region probes from machines you don't run.** Geographic diversity, latency views, uptime SLOs that mean something globally.
3. **Zero ops, zero on-call for the monitor itself.** The monitor must outlive the things it watches; that means *someone else* must operate it.
4. **Meta-monitoring** that's tedious to wire up by hand: TLS/certificate expiry, DNS reachability, certificate transparency log changes.
5. **SSO and identity federation**, for organisations whose operations require it.
6. **Cheaper than the people-time to maintain a self-hosted monitor at scale.** Past trivial volume the engineering hours to maintain Postgres, workers, runbooks, and upgrades exceed the SaaS subscription cost. State this with numbers.

AI / LLM features (anomaly summarisation, log correlation, runbook suggestions) join the named exceptions in Principle 1 *if and when we ship them*: SaaS-only because per-token inference cost makes "free for everyone" impossible. Adding such features requires updating this document.

## What this implies for the codebase

- Two compilation targets:
  - `make build` — OSS binary. Excludes `server/saas/`. Default for self-hosters and the public Docker image.
  - `make build-saas` — SaaS binary, with `//go:build saas` enabling `server/saas/`. Runs on solidping.io only.
- `server/saas/` contains, and only contains:
  - SSO (SAML, OIDC, SCIM) integration.
  - Quota enforcement (rate limits per plan, retention caps, member caps, status-page caps).
  - Billing, plan management, invoicing, spike protection.
  - Multi-tenancy plumbing not shared with self-hosters.
- `server/saas/` subscribes to domain events emitted by `server/`. The OSS code never knows pricing or quotas exist; it just emits "check executed", "user logged in", and the SaaS overlay reacts.
- Frontend mirrors the split: `web/dash0/src/saas/` is the small SSO + plan-management UI surface, included only in SaaS builds.
- No feature flags whose purpose is "OSS user can't do this." Feature flags are for genuine A/B tests or rollout staging. The two carve-outs are *build-time excluded* from the OSS binary, not runtime-toggled.

## Honest opinion: tensions and risks (revised)

The revised model is much cleaner than the original. Dropping the "heavy self-hosters must pay" clause removed the fuzziest, least-enforceable principle. Restricting carve-outs to SSO and quotas is the smallest, most-defensible open-core gate possible. That said, real tensions remain.

1. **Revenue depends entirely on SaaS conversion.** With self-hosting being free forever and only SSO + SaaS hosting being paid, every dollar of revenue must come from someone choosing the SaaS. There is no enterprise self-host tier as a fallback. That is a *clean* bet, but a *single* bet — if SaaS conversion is weaker than projected, there is nowhere else to extract revenue without violating the principles. Validate SaaS conversion early.

2. **SSO-only-on-SaaS excludes a real segment.** Hardcore on-premise enterprise customers — defence, certain healthcare, regulated finance — cannot legally or compliance-ly use SaaS, and so cannot have SSO from us. The trade-off is acceptable *only if* those segments aren't our target. Be explicit internally that we're not pursuing them; otherwise the pressure to add a self-hosted SSO key will appear within the first year.

3. **The viral / bottom-up adoption bet is real but slower for monitoring than for collaboration tools.** Slack, Notion, Figma spread bottom-up because every additional user multiplies the value. Monitoring spreads more slowly: an engineer adds a check for their service, but the check doesn't gain value when their colleague also adds one — the network effect is weaker. Bottom-up still works for monitoring (PagerDuty, Datadog, New Relic all have stories) but the timeline is "quarters" not "weeks." Plan funding accordingly.

4. **Principle 4 needs detection, not just licensing.** A non-compete clause is necessary but not sufficient. We will not realistically sue a small reseller. The mitigation is brand and ergonomics: solidping.io is the canonical option, we ship faster than any reseller could, we own the docs, the integrations, and the community.

5. **Exception creep is the new Principle 1 risk.** The original "no exceptions ever" rule was unsustainable; the revised "two named exceptions" rule is sustainable but requires discipline. Every quarter someone will propose adding audit logs, RBAC, or compliance reports to the SaaS-only list. The discipline: each addition requires updating *this document*. If we end up with eight exceptions, we are an open-core company in denial.

6. **The free-forever self-host means we lose product signals unless telemetry exists.** If we can't see how features are used, we'll over-prioritise what SaaS users do (a non-representative sample). Build optional telemetry from day one, with a clear opt-out, and use it to drive roadmap rather than billing.

7. **"Cannibalisation" is now strategy, not risk.** Easy self-hosting is the funnel. The risk is not that self-hosters cannibalise SaaS revenue — it's that they *never* convert. Mitigate by making the operational argument (Principle 3) and SSO (Principle 1 carve-out) genuinely compelling. If both are weak, conversion stalls.

## Open questions

- Should SSO be available *only* on SaaS, or also as a paid add-on for self-hosters who happen to be in our SaaS-incompatible segments? Saying "SSO requires SaaS" is the simplest message; "SSO requires payment, deployment is your choice" is more accommodating but reintroduces the licence-key complexity we just simplified away. Recommend: stay strict on SaaS-only for now; revisit if a real customer asks loudly.
- Do we ship telemetry on by default? Recommend: yes, anonymous, version + rough volume bucket + feature-use counters, opt-out via a single env var, documented prominently in the README.
- How does the SaaS free tier interact with self-hosting? Today the pricing doc lists a SaaS Free tier (30 endpoints, 10 checks/min). If self-hosting is genuinely friction-free, the SaaS Free tier exists mostly as a try-before-buy funnel — should we keep it, or push everyone to either self-host or pay? Recommend: keep it; it is the friction-free way to evaluate the SaaS pitch (multi-region, zero-ops) without commitment.
- Where does compliance reporting (SOC 2 attestations, audit-log streaming destinations) sit? It is a feature of the SaaS by virtue of where the data lives, but the *code* should remain in the open core. Self-hosters can stream audit logs anywhere they like.
- AI / LLM features: when we ship the first one, this doc must be updated to add it as a named exception under Principle 1.

## Related
- `2026-01-03-saas-pricing.md` — current pricing tiers.
- `2026-05-03-oss-vs-paid-code-separation.md` — research on how peer products implement open-core vs fair-code separation.
