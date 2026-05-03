# How do services like Sentry, Metabase, etc. separate payment-related code from open-source code?

## The question
SolidPing's pricing already maps Free / Pro / Team / Enterprise tiers (see `2026-01-03-saas-pricing.md`). If we want to ship part of the codebase as open source while keeping billing and Enterprise-only features behind a commercial license, how is this typically done by comparable products, and which patterns map cleanly onto a Go backend + Bun/React frontend like ours?

## Three orthogonal axes
Before looking at any specific company, every "open core" project picks a point on three independent axes:

1. **Repo layout** — single repo with internal partition, two repos that compose at build time, or plugin model.
2. **License model** — what each partition is published under: AGPL, MIT, BSL, FSL, Sustainable Use, fully proprietary.
3. **Enforcement mechanism** — how paid features are actually withheld from a freeloader: compile-out at build time, runtime license-key check, hook injection from a private overlay, or pure legal gate with no technical enforcement.

Almost every variation below is a different point in this 3D space.

## Catalogue of real-world approaches

### Sentry — two-repo composition with hook-and-signal patching
- Public repo `getsentry/sentry` is published under the **Functional Source License (FSL)**: source-available, converts to Apache 2.0 after 2 years. Sentry created FSL in late 2023 specifically as a shorter, less variable replacement for BSL.
- Private repo `getsentry/getsentry`. It is a Django app that **imports `sentry` as a library**, adds extra routes/models, then re-exports it. Production sentry.io runs `getsentry`; self-hosters run `sentry`.
- Mechanisms used to keep paid logic out of the public tree:
  - **Django signals as hooks.** `sentry` emits signals like `event_received`. Self-hosted: nothing subscribes. SaaS: `getsentry` subscribes and increments a billing counter.
  - **Swappable backends** like `sentry.nodestore` and `sentry.quotas`. Self-hosted ships a stub, SaaS ships an implementation wired to billing.
  - **Feature-flag handler swap.** `SENTRY_FEATURES` is a hardcoded table in OSS; `getsentry` registers a different handler that flips flags based on the customer's plan and EAP grants.
- Sentry's stated stance: "we are not an open-core company" — all *product* features are in the open repo; only billing/account management and SaaS plumbing are private.

### GitLab — monorepo with mirrored `ee/` tree and module injection
- Single `gitlab-org/gitlab` repo holds both CE (MIT) and EE (proprietary). They merged the previously separate CE/EE repos in 2019.
- Convention: if CE has `lib/gitlab/ldap/`, the EE extension lives at `ee/lib/ee/gitlab/ldap/` — same tree, prefixed by `ee/` and the `EE::` namespace.
- Override technique: write a module in the EE namespace, `prepend` it into the CE class on the **last line** of the CE file. Reason: it minimises CE↔EE merge conflicts because the CE file is touched in exactly one line.
- Licensed feature list lives in `ee/app/models/gitlab_subscriptions/features.rb` and is checked at runtime against the customer's license key.
- Build-time exclusion: setting `FOSS_ONLY=1` (or deleting `ee/`) makes GitLab build as pure CE. License-Finder runs in CI to verify dependency licenses are compatible with both editions.

### Metabase — `enterprise/` directory gated by a Clojure `:ee` build alias
- Same repo, EE code under `enterprise/backend/src/metabase_enterprise/` and `enterprise/frontend/src/metabase-enterprise/`.
- Build-time inclusion: the Clojure CLI alias `:ee` (`clojure -M:ee:run`) adds those paths to the classpath. Without `:ee`, EE code is not even on the classpath.
- Runtime gating: the `metabase.premium-features` namespace validates a license token; without a valid token, EE features refuse to start even when the code is loaded.
- License: AGPL for the OSS tree, source-available "no commercial use without a key" for the `enterprise/` tree.

### PostHog — `ee/` folder *plus* a stripped mirror repo
- Main repo `PostHog/posthog`: most of it MIT, but `ee/` has its own non-MIT `ee/LICENSE`.
- For a strictly MIT build users can either delete `ee/` from a clone or pull `posthog-foss`, an automated mirror that already has `ee/` stripped.
- Runtime gating inside `ee/` decides whether EE features activate.

### n8n — filename/dirname marker (`.ee`)
- Single repo, but every file or directory containing the literal `.ee` (e.g. `something.ee.ts`, an `ee/` dir) is governed by the **n8n Enterprise License** instead of the **Sustainable Use License** that covers the rest.
- Maximum granularity: paid and free features can live as siblings in the same package. The boundary is grep-able and easy to tool around but visually less obvious than a top-level `ee/` directory.

### Cal.com — directory-pinned dual license, philosophy-driven boundary
- `packages/features/ee/` is commercial; everything else is **AGPLv3**.
- Conceptual boundary: "Singleplayer APIs" → AGPL, "Multiplayer APIs" (SSO/SAML/OIDC/SCIM/SIEM, marketplace platform) → commercial. The directory split implements that boundary.

### Mattermost — per-plugin `server/enterprise/` directories
- Whichever directory contains a `LICENSE` file under the **Mattermost Source Available License (SAL)** is the gated zone. Convention: enterprise-only server-side plugin code lives in `server/enterprise/` with the SAL `LICENSE` placed alongside it.
- Runtime: without a valid Enterprise License key, gated code may have reduced functionality or refuse to start. Core repos (`mattermost`, `mattermost-webapp`) keep their existing licenses; the SAL applies only inside the marked directories.

### Grafana — separate proprietary build wrapping OSS via plugin/extension points
- Grafana OSS (AGPLv3 since 2021) is one repo. Grafana Enterprise is a **separate proprietary build** that consumes OSS through plugins and extension points registered during plugin initialization.
- This is the "plugin model" extreme: paid features are not in the OSS repo at all, they are loaded as additional code at runtime. OSS stays pristine; the plugin API becomes a hard contract.

## Patterns extracted

### A. Repo layout
| Pattern | Examples | Tradeoff |
| --- | --- | --- |
| Two repos — EE imports OSS as a library | Sentry, Grafana Enterprise | Cleanest legal/IP separation. Cost: keeping the OSS public surface stable enough for the private side to consume. |
| Monorepo, top-level `ee/` directory | GitLab, Cal.com, PostHog, Metabase, Mattermost | One CI, one PR flow. Need conventions and lint rules to prevent accidental cross-imports OSS→EE. |
| Filename marker (`.ee.`) | n8n | Per-file granularity, very tool-friendly. Blurs the boundary visually. |
| Plugin / extension points | Grafana | OSS stays pristine. The plugin API becomes a hard contract that is expensive to evolve. |

### B. License combinations
- **AGPL + commercial** (Cal.com, Metabase, Grafana OSS): viral copyleft scares competitors from rehosting; commercial license is the escape hatch enterprises buy.
- **MIT + commercial-EE-folder** (PostHog): permissive OSS; rely on the EE gate alone to monetise.
- **FSL only** (Sentry): single license, source-available, time-converts to Apache 2.0/MIT after 2 years. Avoids needing two licenses at all.
- **BSL** (HashiCorp pre-fork, MariaDB, CockroachDB): older idea, default 4-year non-compete, per-vendor variability via the Additional Use Grant.
- **Sustainable Use + Enterprise** (n8n): two licenses split at file-name level.

### C. Enforcement
1. **Build-time exclusion** — strip `ee/`, set `FOSS_ONLY=1`, omit a build alias, use Go build tags. The unpaid binary literally cannot run paid features.
2. **Runtime license key** — same binary, behaviour switches when a signed license file is present (GitLab EE keys, Mattermost SAL, Metabase premium token, PostHog license).
3. **Hook injection** (Sentry-style) — OSS exposes signals/hooks; the SaaS overlay subscribes. Self-hosted users keep an inert hook.
4. **Pure legal gate** — code is technically there, only the license forbids commercial use (BSL/FSL non-compete window). No technical enforcement.

Most projects combine #1 and #2 — strip at build time for the public OSS distribution, key-check at runtime so a single binary can serve all paid plans.

## Mapping onto SolidPing
A Go + Bun/React stack maps very naturally to the GitLab/Cal.com pattern, with a Sentry-style overlay for billing:

- **Backend**: keep a `server/ee/` Go package gated by `//go:build enterprise` and a `make build-ee` target. The public surface of `server/` exposes hooks (interfaces / function variables) the EE package wires into during `init()`. The default Free build never imports `ee/`, so the binary cannot run a paid feature even if a user crafts a fake license key.
- **Frontend**: mirror the split — `web/dash0/src/ee/` shipped only when the build is for the SaaS or for an Enterprise-licensed self-host. Vite/Bun aliases let `@/ee/...` resolve to the real module on a SaaS build and to a stub on the OSS build.
- **License options**: either **AGPLv3 + Commercial** (Cal.com / Metabase shape — strong copyleft makes free-rider rehosting hard) or **FSL** (Sentry shape — single license, no per-feature gymnastics, but not OSI-approved).
- **Billing**: keep it 100% out of the OSS tree, Sentry-style. OSS emits domain events ("check executed", "incident opened"); the SaaS-only billing module subscribes and counts. The OSS code never knows pricing exists.
- **Tier features that are mostly numeric** (1-second minimum interval, custom domains, retention windows, member counts, status-page counts): implement them as plan-driven *config* in the OSS code, with the defaults being the Free-tier limits. The SaaS overrides those values per customer plan. This keeps the EE-specific *code paths* small and the gating mostly in *configuration*, which dramatically reduces the EE surface area.

## Open sub-questions
- Do we want **self-hosted Enterprise** to be a paying option, or are paid features SaaS-only? GitLab/Sentry/Metabase all support self-hosted EE with a license key. PostHog ships every paid feature in self-host but expects payment past a usage threshold.
- Are billing/quotas the *only* thing in the closed component (Sentry's purist stance), or do we accept some product features being paid-only (everyone else's stance)? Our pricing already implies the latter — custom domains, longer retention, larger team counts are tier-gated product features.
- License choice locks future hires and contributors. AGPL is friendlier to OSS purists but pushes large customers to negotiate. FSL is simpler operationally but is not OSI-approved and is still controversial in the community.
- Where does the OSS↔EE boundary sit for SolidPing-specific things like the worker protocol, MCP endpoint, regions, badges? These should probably stay on the OSS side because their value is the ecosystem, not the gate.

## What is actually behind the gate? Top features per product

For each product below: the paid-tier names, the headline features that are gated, and whether each gated feature is *self-hostable with a key* (you can run it on your own infra after buying a license) or *SaaS-only* (cannot be run on self-hosted at any price).

### Sentry — Team / Business / Enterprise (SaaS) vs Self-Hosted (free)
Sentry's stance is *almost everything product-side ships in OSS*. The closed `getsentry` overlay is mostly the SaaS plumbing.

| # | Feature | Self-host with key? | SaaS-only? |
| - | --- | --- | --- |
| 1 | Pricing tiers, billing, quotas, invoicing | — | SaaS-only (no key sold) |
| 2 | Spike protection (event-rate cap that prevents bill blowouts) | — | SaaS-only — coupled to billing |
| 3 | Spend allocation (per-project quota carve-outs) | — | SaaS-only — coupled to billing |
| 4 | Seer and AI/ML features (issue auto-fix, autofix suggestions) | — | SaaS-only — closed source |
| 5 | iOS symbolication using Apple's symbol server | — | SaaS-only — Apple does not provide a public symbol server |
| 6 | SSO/SAML, audit logs, advanced data scrubbing | Free in self-host (already in OSS) | Available on Business/Enterprise SaaS |

Note: Sentry does **not** sell a self-hosted Enterprise license. Self-hosters get the OSS feature set; SaaS customers pay for hosting + the SaaS-only features above.

### GitLab — Free (self-host CE) / Premium / Ultimate (both self-host and SaaS, same per-seat price)
GitLab's gated set is the largest of the bunch — 77+ Ultimate-only features on top of Premium.

**Top Premium-gated** (≈$29/user/month, both self-host and SaaS):
1. Merge request approvals + Code Owners enforcement.
2. Multi-level epics, roadmaps, portfolio planning.
3. Advanced CI/CD: protected environments, deploy approvals, multi-project pipelines.
4. Group/instance-level audit events.
5. SLA-backed support, plus 10k CI minutes & 50 GB storage on SaaS.

**Top Ultimate-gated** (custom pricing, both):
1. SAST / DAST / Dependency / Container / IaC / Secret detection scanners + security dashboards.
2. License-compliance scanning (dependency licenses).
3. Compliance frameworks, compliance pipelines, compliance dashboards.
4. Vulnerability management workflow & security policies.
5. Free Guest seats (Guests don't consume seats on Ultimate — a billing carrot, not a feature).

Everything Premium/Ultimate works the same on self-managed and on GitLab.com; the license key flips it.

### Metabase — Open Source (free) / Pro / Enterprise (both self-host and Cloud)
1. **Data sandboxing** — row-level security driven by user attributes. Enterprise-only on both self-host and Cloud.
2. **SSO / SAML / SCIM** + JWT SSO for embedding. Available on Pro and Enterprise.
3. **Audit logs / Usage analytics** (queries, dashboards, permission changes). Pro and Enterprise.
4. **Advanced/granular permissions**: column-level, native query exec rights, application permissions per group. Pro and Enterprise.
5. **Whitelabeling, premium embedding, official collections, content moderation, serialization for env promotion**. Pro and Enterprise.

All Pro/Enterprise features are buyable as a key for self-hosted. Metabase Cloud just bundles hosting on top.

### PostHog — Free / Teams ($) / Enterprise (both self-host and Cloud)
1. **SSO enforcement, SAML 2.0, SCIM provisioning** — Enterprise only.
2. **Role-based access control (RBAC)** with custom roles per org. Enterprise only.
3. **Audit logs** + change requests / governance. Enterprise only.
4. **Multiple environments / unlimited projects** with isolation. Enterprise only.
5. **HIPAA BAA, white-labeling, priority support, dedicated CSM**. Enterprise only.

PostHog's twist: every paid feature is in the `ee/` directory and *technically runs* on self-host, but the `ee/LICENSE` requires a paid agreement once you exceed usage thresholds or use Enterprise features in production.

### n8n — Community (free, self-host) / Starter / Pro / Business / Enterprise (Cloud + self-host)
1. **SSO via SAML / LDAP / OIDC** — Enterprise only.
2. **RBAC with projects** — workflows grouped into projects with per-project roles. Enterprise only (basic role separation exists in lower tiers).
3. **Audit logs + log streaming to external SIEM** — Enterprise only.
4. **Git-based version control / environments** — push/pull workflows to Git, dev → prod promotion. Enterprise only.
5. **External secret stores** (Vault, AWS Secrets Manager, etc.) + isolated worker queues + scaled concurrency. Enterprise only.

All Enterprise features available on self-host with a key (this is n8n's main differentiator vs locked SaaS competitors). Files/dirs containing `.ee` carry the Enterprise License.

### Cal.com — Free / Teams ($15) / Organizations ($37) / Enterprise (both self-host and Cloud)
1. **SAML SSO + SCIM provisioning** — Organizations and Enterprise.
2. **Domain-wide delegation, sub-team structures, RBAC** — Organizations and Enterprise.
3. **HIPAA-ready / SOC 2 / ISO 27001** compliance posture (BAA on Enterprise) — Organizations+/Enterprise.
4. **Audit logs** + admin controls — Organizations and Enterprise.
5. **White-labeled platform / API platform plan** for building your own scheduling marketplace — Enterprise (Platform plan).

Code is all in `packages/features/ee/` under a Commercial license; running it commercially requires a key whether self-hosted or on Cal.com Cloud.

### Mattermost — Team (free) / Professional (formerly E10) / Enterprise (formerly E20)
1. **AD / LDAP sync** with group sync to roles/teams/channels — Professional+. **AD/LDAP Groups** (department/security-classification groupings) is Enterprise-only.
2. **SAML 2.0 SSO** (Okta, OneLogin, ADFS) — Enterprise only.
3. **Guest accounts** with SAML/LDAP-authenticated guests — Professional+ (with extra controls in Enterprise).
4. **Compliance exports, data retention policies, advanced audit logging** — Enterprise only.
5. **High-availability cluster, performance monitoring, advanced permission schemes** — Enterprise only.

License-key gated at runtime; the same binary runs Free/Professional/Enterprise depending on the key. Code is in directories carrying the `LICENSE` file under the Mattermost Source Available License.

### Grafana — OSS (AGPLv3) / Enterprise (self-host) / Cloud (SaaS, multiple tiers)
1. **Enterprise data-source plugins** — Splunk, Snowflake, Oracle, Dynatrace, ServiceNow, Datadog, MongoDB, Salesforce, etc. (the OSS edition only has the basic data sources). Enterprise only.
2. **RBAC with fine-grained / fixed roles** beyond OSS's three basic roles (Viewer/Editor/Admin). Enterprise/Cloud.
3. **Data source permissions** (which users/teams can query which DS). Enterprise/Cloud.
4. **Reporting / scheduled PDF exports / email reports** — Enterprise/Cloud.
5. **White-labeling**, **request security & rate limiting**, **usage insights**, **SAML SSO / team sync**, **vault encryption with KMS**. Enterprise/Cloud.

Self-hostable with a key (Grafana Enterprise binary). Cloud bundles hosting + tier features on top.

## Cross-product pattern: what is *always* paid?
Looking at all eight products together, the same handful of features show up behind every paywall. If you build a SaaS in 2026 and follow industry norms, these are the safe candidates for the paid tier:

1. **SAML / SSO / SCIM provisioning.** Universally paid. Customers expect this; it is also the easiest enterprise sale.
2. **Fine-grained RBAC / custom roles / project-level permissions.** Universal.
3. **Audit logs + log streaming to a SIEM.** Universal.
4. **Compliance posture: HIPAA BAA, SOC 2 / ISO 27001 attestations, data residency, data retention controls.** Universal.
5. **White-labeling / custom branding / custom domains.** Almost universal.
6. **SLA-backed support, dedicated CSM, priority response.** Universal.
7. **Quotas, billing, usage caps, spike protection.** *SaaS-only* in every product — these are intrinsic to the SaaS business model and cannot be sold as a self-host feature.
8. **Premium connectors / data sources / integrations** (Grafana, n8n, Metabase). Sometimes paid.
9. **High-availability / clustering / horizontal scale features**. Often paid (Mattermost, n8n).
10. **AI/ML features.** Increasingly SaaS-only (Sentry Seer, GitHub Copilot pattern). Operating cost is real and self-hosters can't be billed for inference.

## Implication for SolidPing
Given our pricing already gates **member count, status-page count, retention, custom domains, minimum check interval**, those map onto the *config-knob* pattern (numeric defaults that the SaaS overrides per plan) — they don't need to live in `ee/` at all. The OSS code can know about the limits; the SaaS just sets bigger numbers.

What *does* belong in `ee/` if we follow the industry pattern:
- SAML/OIDC SSO, SCIM, RBAC, audit log streaming → Pro/Team/Enterprise tier features.
- Billing/quotas/spike protection → SaaS-only (Sentry-style overlay).
- Premium notification channels (e.g. PagerDuty, Opsgenie integrations beyond the basic ones), if we ever build them → tier-gated.
- Compliance reports, log retention beyond OSS defaults → tier-gated.
- White-labeled status pages, custom domains → already in pricing.

## Pointers
- Sentry architecture: https://develop.sentry.dev/application-architecture/sentry-vs-getsentry/
- Sentry SaaS vs self-hosted differences: https://sentry.zendesk.com/hc/en-us/articles/39647157386139
- FSL: https://fsl.software/
- GitLab EE guidelines: https://docs.gitlab.com/development/ee_features/
- GitLab feature comparison: https://about.gitlab.com/pricing/feature-comparison/
- Metabase Pro/Enterprise: https://www.metabase.com/product/enterprise
- Metabase enterprise tree: github.com/metabase/metabase under `enterprise/`
- Cal.com `ee/` license: github.com/calcom/cal.com/blob/main/packages/features/ee/LICENSE
- Cal.com Enterprise: https://cal.com/enterprise
- PostHog `ee/` license: github.com/PostHog/posthog/blob/master/ee/LICENSE
- PostHog SSO/SAML/SCIM: https://posthog.com/docs/settings/sso
- n8n Sustainable Use License: https://docs.n8n.io/sustainable-use-license/
- n8n Enterprise feature list: https://n8n.io/pricing/
- Mattermost Source Available License: https://docs.mattermost.com/product-overview/faq-mattermost-source-available-license.html
- Mattermost editions: https://docs.mattermost.com/product-overview/editions-and-offerings.html
- Grafana OSS vs Enterprise: https://grafana.com/oss-vs-cloud/
- Grafana RBAC: https://grafana.com/docs/grafana/latest/administration/roles-and-permissions/access-control/
