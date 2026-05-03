# Business Vision

This document records the business posture of SolidPing — what we charge for, what we don't, and why. It exists to constrain product, license, and architecture decisions: if a future change is in tension with one of these principles, the change requires re-opening this document, not a quiet one-off feature gate.

## The four principles

### 1. Core product is fully featured for everyone — no artificial degradation
We do not gate SSO, RBAC, audit logs, retention windows, integration count, status pages, or any other product capability behind a paid tier *for the purpose of pushing people to pay*. If a feature exists, every user gets it. Where limits exist (e.g., a free SaaS tier check frequency), they reflect honest operational cost, not coercion.

This is the Sentry stance ("we are not an open-core company") and it is deliberate. Most peer products gate SSO/RBAC/audit specifically to extract enterprise dollars. We don't.

### 2. Heavy self-hosting use should be paid use
SolidPing is free to run for personal projects, side businesses, internal tools, and any use that doesn't represent a serious commercial dependency. Once an organization runs SolidPing as load-bearing commercial infrastructure — high check rates, large team counts, mission-critical alerting — it is expected to acquire a Self-Hosted Commercial License.

The license does **not** unlock additional features (see Principle 1). It is a legal grant for heavy commercial use, in the spirit of n8n's Sustainable Use License or BSL-style non-compete grants.

### 3. SaaS hosting is paid, and recommended
We charge for SolidPing.io. We also actively recommend it over self-hosting, because **monitoring should not run on the same infrastructure it monitors**. If your application's database goes down and your Postgres-backed monitor goes down with it, the monitor failed at its only job. Externalising monitoring to a service with independent failure domains is operations hygiene, not a sales pitch.

Pricing tiers are defined in `2026-01-03-saas-pricing.md` and calibrated to make SaaS the easy default for most customers.

### 4. Nobody else may charge to host SolidPing as a SaaS
The license forbids third-party hosted "SolidPing-as-a-Service" offerings. AWS-style arbitrage — "we host your open-source product cheaper than you" — is the failure mode that pushed Mongo, Elastic, Redis, HashiCorp, and Sentry off OSI-approved licenses. We adopt the same posture from day one.

This means SolidPing is **not OSI Open Source**. It is *fair-code* / source-available with a non-compete clause.

## What this implies for the license

The four principles converge on a fair-code license with a SaaS non-compete clause, with most code time-converting to permissive OSS after a delay. Two viable choices:

- **FSL-1.1-Apache-2.0** (Sentry, Liquibase). Source-available, 2-year non-compete on competing offerings, then auto-converts to Apache 2.0. Single license, predictable, well-understood.
- **n8n-style Sustainable Use License**. Fair-code license with explicit non-compete on hosting. Less relevant to file-level splitting for us *because* Principle 1 means there are no EE-locked features to put in an `ee/` directory.

The "heavy commercial use must be paid" clause from Principle 2 has to be added on top of either, since no off-the-shelf license cleanly combines all four principles. Practical wording: "free for use by any individual or organization for non-production / low-volume / non-mission-critical use; production use over [thresholds] requires a Self-Hosted Commercial License."

## What this implies for self-hosting

- Self-hosters get **the same binary** as the SaaS, fully featured.
- Soft enforcement only: the binary may emit telemetry indicating volume and may surface a polite "you're now in Pro/Team/Enterprise territory, please buy a license" UI banner. No hard block. An opt-out env var must exist for paranoid environments.
- Hard enforcement is reserved for the license clause itself, enforceable in court against organizations that care about license compliance — which is the segment we want to convert anyway. Enforcement against pirates and freeloaders is not a goal.
- The Self-Hosted Commercial License is a contract, not a feature flag. There is no license-key file that unlocks anything because there is nothing to unlock.

## What this implies for SaaS

- SaaS pricing has to be defensible against entrenched competition (UptimeRobot's free tier, Better Stack, Hyperping, Pingdom, Datadog Synthetics).
- The pitch is **not** "more features than self-hosted" — that contradicts Principle 1. The pitch is:
  - **Independent failure domain.** SolidPing.io stays up when your stack goes down.
  - **Multi-region probing** that is hard to recreate with self-hosted workers in one DC.
  - **Zero ops.** No Postgres to manage, no migrations, no version upgrades, no on-call rotation for the thing that's supposed to wake you up.
  - **Meta-monitoring** (TLS expiry, certificate transparency, DNS reachability) that's annoying to do for yourself.
- AI / LLM features (e.g. anomaly summarization, log correlation) are an explicit exception to Principle 1: SaaS-only when the cost structure (per-token inference) makes "free for everyone" actually impossible. This carve-out is honest and customers understand it.

## What this implies for the codebase

If Principle 1 holds strictly:
- There are no `ee/` directories with locked product features. Existing `server/` and `web/dash0/src/` carry everything.
- A `server/saas/` package can exist for billing, quota enforcement, spike protection, and SaaS-only AI features — Sentry-style overlay subscribing to domain events emitted by `server/`.
- Build tag `//go:build saas` controls inclusion of `server/saas/`. The default `make build` target produces the OSS binary; `make build-saas` produces the binary that runs on solidping.io.
- Zero feature flags exist whose purpose is "OSS user can't do this." Feature flags are reserved for genuine A/B tests or rollout staging.

If we ever soften Principle 1 — for example, deciding that some compliance feature is genuinely worth gating — that is a Vision-level change requiring an update to this document.

## Honest opinion: tensions and risks

The four principles are coherent and unusual. Most peer products break Principle 1 *specifically* to make Principle 2 enforceable: feature-gating is the easiest way to get heavy users to pay, because they self-select by needing SSO. Choosing not to do that has real costs.

1. **Conversion is harder.** When an enterprise hits "we need SSO," every peer product converts them by charging for SSO. Our equivalent is "we need a Self-Hosted Commercial License because we're now load-bearing." That's a procurement conversation, not a checkout button. Expect a longer, more relationship-driven enterprise sales motion than feature-gated competitors.

2. **"Heavy use" is a fuzzy line.** Where exactly does free use end and the Self-Hosted Commercial License begin? Pick concrete thresholds in the license (e.g. >100 endpoints OR >10 team members OR mission-critical use defined as triggering paging) and accept that the line will be argued. Vague clauses produce free riders; over-strict clauses scare away the SMBs we want as community.

3. **No-OSI cost is real but manageable.** "Not OSI Open Source" excludes us from some communities (Debian main, Fedora, parts of the OSS purity discourse). Sentry, n8n, MongoDB, Elastic, HashiCorp, Redis all survived this transition. Community pushback is loudest in the first 6 months and then fades.

4. **Principle 4 needs detection, not just licensing.** A clause forbidding AWS-style hosting is necessary but not sufficient. We will not realistically sue a small reseller. The mitigation is brand and ergonomics: make solidping.io obviously the canonical option, ship faster than any reseller can, own the docs and integrations.

5. **The SaaS pitch needs sharpening.** "Externalize your monitoring" is correct but generic. The strongest authentic version: *"if your app and your monitor share an outage, your monitor failed."* This is a real architectural argument, not marketing. Lead with it.

6. **Principle 1 is the hardest to keep.** Every quarter, someone will propose gating a feature for revenue. Every quarter, this document should be the answer to why we don't. If we revisit it, we revisit it explicitly — not by accident through six small product decisions.

7. **The honest restatement of Principle 2:** "we trust enterprises with procurement teams and budget cycles to comply; we don't expect to extract money from solo devs or pirates." That's a defensible business *if* an enterprise segment for SolidPing actually exists. Validating that — that uptime monitoring with our specific differentiators has real enterprise pull — is a separate question this document does not answer.

8. **Cannibalization risk.** A simple, easy-to-self-host Go binary with SQLite is a low-friction self-host story. Compare to Sentry, where self-hosting is a 16 GB RAM Docker stack — friction itself converts users. If SolidPing self-hosts in 30 seconds, our SaaS pitch leans entirely on the failure-domain argument, not on operational pain.

## Open questions

- What are the exact thresholds in the Self-Hosted Commercial License clause? "Heavy use" needs concrete numbers.
- Do we ship telemetry by default? If yes, what does it report and how do we make the opt-out trivially discoverable?
- What is the canonical phrasing of Principle 4 in the license file? Borrow from FSL, n8n SUL, or BSL?
- Do AI/LLM features get their own document, or do we inherit Principle 1 with a SaaS-only carve-out?
- Where does compliance reporting (SOC 2 attestations, audit-log streaming destinations) sit — included for everyone, or carved out alongside AI as a SaaS-cost feature?
- Does SaaS get a permanent free tier, or is the free path "self-host the binary"? The pricing doc currently implies a SaaS Free tier — keeping it makes the funnel work but adds operational cost.

## Related
- `2026-01-03-saas-pricing.md` — current pricing tiers.
- `2026-05-03-oss-vs-paid-code-separation.md` — research on how peer products implement open-core vs fair-code separation.
