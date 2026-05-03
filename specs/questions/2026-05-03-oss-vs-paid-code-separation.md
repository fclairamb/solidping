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

## Pointers
- Sentry architecture: https://develop.sentry.dev/application-architecture/sentry-vs-getsentry/
- FSL: https://fsl.software/
- GitLab EE guidelines: https://docs.gitlab.com/development/ee_features/
- Metabase enterprise tree: github.com/metabase/metabase under `enterprise/`
- Cal.com `ee/` license: github.com/calcom/cal.com/blob/main/packages/features/ee/LICENSE
- PostHog `ee/` license: github.com/PostHog/posthog/blob/master/ee/LICENSE
- n8n Sustainable Use License: https://docs.n8n.io/sustainable-use-license/
- Mattermost Source Available License: https://docs.mattermost.com/product-overview/faq-mattermost-source-available-license.html
